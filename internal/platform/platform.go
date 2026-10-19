// SPDX-License-Identifier: GPL-3.0-only
package platform

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/store"
)

func DataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "My Code Dashboard"), nil
}
func SecureDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("data directory must be a real directory")
	}
	return os.Chmod(path, 0700)
}

type Lock struct{ file *os.File }

func Acquire(dir string) (*Lock, error) {
	if err := SecureDir(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "application.lock")
	if st, err := os.Lstat(path); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("lock path must not be a symlink")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another application process is already using this data directory")
	}
	return &Lock{file: f}, nil
}
func (l *Lock) Close() error {
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

type Manifest struct {
	Format              int       `json:"format"`
	Version             string    `json:"version"`
	CreatedAt           time.Time `json:"created_at"`
	CredentialsIncluded bool      `json:"credentials_included"`
}

func Export(ctx context.Context, s *store.Store, w io.Writer, version string) error {
	dir, err := os.MkdirTemp(filepath.Dir(s.Path), "backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	database := filepath.Join(dir, "dashboard.sqlite")
	if err = s.Backup(ctx, database); err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	metadata, _ := json.MarshalIndent(Manifest{Format: 1, Version: version, CreatedAt: time.Now().UTC()}, "", "  ")
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(metadata))}); err == nil {
		_, err = tw.Write(metadata)
	}
	if err == nil {
		var f *os.File
		f, err = os.Open(database)
		if err == nil {
			info, e := f.Stat()
			if e != nil {
				err = e
			} else {
				err = tw.WriteHeader(&tar.Header{Name: "dashboard.sqlite", Mode: 0600, Size: info.Size()})
				if err == nil {
					_, err = io.Copy(tw, f)
				}
			}
			f.Close()
		}
	}
	if e := tw.Close(); err == nil {
		err = e
	}
	if e := gz.Close(); err == nil {
		err = e
	}
	return err
}

// Restore requires the caller to hold the installation lock. Archive contents
// are allowlisted and validated before the existing database is touched.
func Restore(dir, archive string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tmp, err := os.MkdirTemp(dir, "restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	tr := tar.NewReader(io.LimitReader(gz, 512<<20))
	seen := map[string]bool{}
	var manifest Manifest
	for {
		h, e := tr.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return errors.New("invalid or oversized backup archive")
		}
		if h.Typeflag != tar.TypeReg || seen[h.Name] || (h.Name != "manifest.json" && h.Name != "dashboard.sqlite") || h.Size < 0 || h.Size > 500<<20 {
			return errors.New("backup contains an unexpected, duplicate, or oversized entry")
		}
		seen[h.Name] = true
		if h.Name == "manifest.json" {
			if h.Size > 65536 {
				return errors.New("backup manifest is too large")
			}
			if e = json.NewDecoder(tr).Decode(&manifest); e != nil {
				return errors.New("invalid backup manifest")
			}
		} else {
			out, e := os.OpenFile(filepath.Join(tmp, "dashboard.sqlite"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			_, e = io.Copy(out, tr)
			out.Close()
			if e != nil {
				return e
			}
		}
	}
	if !seen["manifest.json"] || !seen["dashboard.sqlite"] || manifest.Format != 1 || manifest.CredentialsIncluded {
		return errors.New("unsupported or incomplete backup")
	}
	// Read through the gzip trailer so interrupted/corrupt exports cannot bypass
	// checksum validation merely because the tar end markers were already read.
	trailing, readErr := io.ReadAll(io.LimitReader(gz, 1025))
	if readErr != nil || len(trailing) > 0 {
		return errors.New("backup has invalid compressed data or trailing content")
	}
	if err = store.ValidateBackup(filepath.Join(tmp, "dashboard.sqlite")); err != nil {
		return err
	}
	return restoreDatabase(filepath.Join(tmp, "dashboard.sqlite"), filepath.Join(dir, "dashboard.sqlite"))
}

func restoreDatabase(candidatePath, target string) (err error) {
	suffixes := []string{"", "-wal", "-shm", "-journal"}
	hasPrevious := false
	for _, suffix := range suffixes {
		info, e := os.Lstat(target + suffix)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return errors.New("existing database and SQLite sidecars must be regular files")
		}
		hasPrevious = true
	}
	destinationID := model.NewID()
	// Read without migration: corrupt or newer databases must not block recovery.
	if settings, e := store.ReadSettings(target); e == nil {
		if id, e := hex.DecodeString(settings.InstallationID); e == nil && len(id) == 16 {
			destinationID = settings.InstallationID
		}
	}
	// A restore into another installation must not adopt its Keychain namespace
	// or LaunchAgent identity. Preserve a readable, valid destination identity.
	candidate, err := store.Open(candidatePath)
	if err != nil {
		return err
	}
	settings, err := candidate.Settings()
	if err == nil {
		settings.InstallationID = destinationID
		err = candidate.SaveSettings(settings)
	}
	if err == nil {
		var connections []model.Connection
		connections, err = candidate.Connections()
		for _, connection := range connections {
			if err != nil {
				break
			}
			// Imported IDs can collide with provisioned files or destination
			// Keychain entries belonging to a different provider host.
			connection.ReconnectRequired = true
			connection.Status = "disconnected"
			connection.Message = "Restored connection. Reconnect and validate a credential before refreshing."
			err = candidate.SaveConnection(connection)
		}
	}
	closeErr := candidate.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !hasPrevious {
		return os.Rename(candidatePath, target)
	}
	previous, err := os.MkdirTemp(filepath.Dir(target), "before-restore-"+time.Now().UTC().Format("20060102T150405")+"-")
	if err != nil {
		return err
	}
	previousDB := filepath.Join(previous, "dashboard.sqlite")
	var moved []string
	defer func() {
		if err == nil {
			return
		}
		for i := len(moved) - 1; i >= 0; i-- {
			suffix := moved[i]
			if rollbackErr := os.Rename(previousDB+suffix, target+suffix); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("recover previous database files from %s: %w", previous, rollbackErr))
			}
		}
	}()
	for _, suffix := range suffixes {
		if err = os.Rename(target+suffix, previousDB+suffix); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		moved = append(moved, suffix)
	}
	return os.Rename(candidatePath, target)
}

const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>{{.Label}}</string>
<key>ProgramArguments</key><array><string>{{.Binary}}</string><string>serve</string><string>--data-dir</string><string>{{.Dir}}</string><string>--addr</string><string>{{.Addr}}</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>15</integer>
<key>Umask</key><integer>63</integer>
<key>StandardOutPath</key><string>{{.Dir}}/service.log</string>
<key>StandardErrorPath</key><string>{{.Dir}}/service.log</string>
</dict></plist>`

func ServiceLabel(installation string) string {
	if len(installation) > 8 {
		installation = installation[:8]
	}
	return "org.olegiv.my-code-dashboard." + installation
}
func ServicePath(installation string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", ServiceLabel(installation)+".plist"), nil
}

func InstallService(ctx context.Context, dir, addr, installation string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("LaunchAgent management is available on macOS; see the Linux service example")
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	binary := filepath.Join(binDir, "my-code-dashboard")
	if self != binary {
		src, err := os.Open(self)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.CreateTemp(binDir, "binary-")
		if err != nil {
			return err
		}
		name := dst.Name()
		defer os.Remove(name)
		_, err = io.Copy(dst, src)
		if err == nil {
			err = dst.Chmod(0755)
		}
		dst.Close()
		if err != nil {
			return err
		}
		if err = os.Rename(name, binary); err != nil {
			return err
		}
	}
	path, err := ServicePath(installation)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	var xml strings.Builder
	if err = renderPlist(&xml, dir, addr, installation); err != nil {
		return err
	}
	if err = os.WriteFile(path, []byte(xml.String()), 0600); err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	// Never terminate the running server while it is answering an HTTP request.
	// A loaded agent picks up the replacement binary at its next process start.
	if exec.CommandContext(ctx, "/bin/launchctl", "print", domain+"/"+ServiceLabel(installation)).Run() == nil {
		return nil
	}
	if err = exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", domain, path).Run(); err != nil {
		return errors.New("LaunchAgent was saved but could not be loaded; log in again to start it")
	}
	return nil
}
func renderPlist(w io.Writer, dir, addr, installation string) error {
	t := template.Must(template.New("plist").Parse(plist))
	return t.Execute(w, map[string]string{"Label": ServiceLabel(installation), "Binary": filepath.Join(dir, "bin", "my-code-dashboard"), "Dir": dir, "Addr": addr})
}
func RemoveService(ctx context.Context, installation string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("LaunchAgents are available on macOS only")
	}
	path, err := ServicePath(installation)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
func StopService(ctx context.Context, installation string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("LaunchAgents are available on macOS only")
	}
	label := fmt.Sprintf("gui/%d/%s", os.Getuid(), ServiceLabel(installation))
	// A missing/unloaded agent already satisfies this operation.
	if exec.CommandContext(ctx, "/bin/launchctl", "print", label).Run() != nil {
		return nil
	}
	if exec.CommandContext(ctx, "/bin/launchctl", "bootout", label).Run() != nil {
		return errors.New("could not unload the LaunchAgent")
	}
	return nil
}
