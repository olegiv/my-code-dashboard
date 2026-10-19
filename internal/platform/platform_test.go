// SPDX-License-Identifier: GPL-3.0-only
package platform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/store"
)

type backupWriter struct {
	data               bytes.Buffer
	writeErr, closeErr error
	closes             int
}

func (w *backupWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.data.Write(p)
}
func (w *backupWriter) Close() error { w.closes++; return w.closeErr }

func TestBackupCopyPropagatesCloseErrors(t *testing.T) {
	copyErr, closeErr := errors.New("synthetic copy failure"), errors.New("synthetic close failure")
	for _, tc := range []struct {
		name               string
		writeErr, closeErr error
		want               error
	}{
		{"normal", nil, nil, nil},
		{"copy", copyErr, nil, copyErr},
		{"close", nil, closeErr, closeErr},
		{"copy and close", copyErr, closeErr, copyErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &backupWriter{writeErr: tc.writeErr, closeErr: tc.closeErr}
			if err := copyAndClose(w, strings.NewReader("synthetic database bytes")); !errors.Is(err, tc.want) {
				t.Fatalf("got %v; want %v", err, tc.want)
			}
			if w.closes != 1 || tc.writeErr == nil && w.data.String() != "synthetic database bytes" {
				t.Fatal("copy did not write expected bytes and close exactly once")
			}
		})
	}
}

func TestInstallPlistAtomically(t *testing.T) {
	if path := os.Getenv("MCD_PLIST_FAULT_PATH"); path != "" {
		// Only this child process receives a file-size limit. Never touch a
		// real LaunchAgent, host resource limits, or service configuration.
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 128, Max: 128}); err != nil {
			t.Fatal(err)
		}
		if err := writePlist(path, bytes.Repeat([]byte("x"), 4096)); !errors.Is(err, syscall.EFBIG) {
			t.Fatalf("expected a real partial-write size-limit failure, got %v", err)
		}
		return
	}
	for _, name := range []string{"new", "replacement", "partial write"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "service.plist")
			before := []byte("<plist>previous definition</plist>")
			if name != "new" {
				if err := os.WriteFile(path, before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			want := []byte("<plist>replacement definition</plist>")
			if name == "partial write" {
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestInstallPlistAtomically$")
				cmd.Env = []string{"MCD_PLIST_FAULT_PATH=" + path}
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("fault child failed: %v\n%s", err, output)
				}
				want = before
			} else if err := writePlist(path, want); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("installed definition changed unexpectedly: %q, %v", got, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("definition is not private", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatal("publication left temporary files", err)
			}
		})
	}
}

func TestLockAndIndependentDirectories(t *testing.T) {
	dir := t.TempDir()
	a, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Acquire(dir); err == nil {
		t.Fatal("overlapping processes accepted")
	}
	b, err := Acquire(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	a.Close()
	c, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("installation directory is not private")
	}
	linked := filepath.Join(t.TempDir(), "link")
	if err = os.Symlink(dir, linked); err != nil {
		t.Fatal(err)
	}
	if _, err = Acquire(linked); err == nil {
		t.Fatal("symlink data directory accepted")
	}
}
func TestBackupRoundTripAndRejectedArchivePreservesDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dashboard.sqlite")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	connection := model.Connection{ID: "synthetic", Name: "Before backup", Provider: "github", BaseURL: "https://github.com"}
	if err = s.SaveConnection(connection); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err = Export(context.Background(), s, &buf, "0.1.0-test"); err != nil {
		t.Fatal(err)
	}
	originalSettings, _ := s.Settings()
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err = os.WriteFile(archive, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	connection.Name = "After backup"
	_ = s.SaveConnection(connection)
	s.Close()
	if err = Restore(dir, archive); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.Connection("synthetic")
	s.Close()
	if c.Name != "Before backup" {
		t.Fatal("restore did not recover database snapshot")
	}
	if !c.ReconnectRequired || c.Status != "disconnected" {
		t.Fatal("restore can reuse an existing credential without explicit revalidation")
	}
	newDir := t.TempDir()
	if err = Restore(newDir, archive); err != nil {
		t.Fatal(err)
	}
	newSettings, err := store.ReadSettings(filepath.Join(newDir, "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if newSettings.InstallationID == originalSettings.InstallationID {
		t.Fatal("restore into another installation shared the credential namespace")
	}
	corrupted := append([]byte(nil), buf.Bytes()...)
	corrupted[len(corrupted)-1] ^= 255
	corruptArchive := filepath.Join(t.TempDir(), "corrupt.tar.gz")
	if err = os.WriteFile(corruptArchive, corrupted, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Restore(dir, corruptArchive); err == nil {
		t.Fatal("corrupt gzip checksum accepted")
	}
	copies, _ := filepath.Glob(filepath.Join(dir, "before-restore-*"))
	if len(copies) != 1 {
		t.Fatal("previous database was not saved")
	}
	for _, name := range []string{"../escape", "manifest.json", "dashboard.sqlite"} {
		var bad bytes.Buffer
		gz := gzip.NewWriter(&bad)
		tw := tar.NewWriter(gz)
		body := []byte(`{"format":1,"credentials_included":false}`)
		_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(body))})
		_, _ = tw.Write(body)
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 0})
		_ = tw.Close()
		_ = gz.Close()
		if err = os.WriteFile(archive, bad.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if err = Restore(dir, archive); err == nil {
			t.Fatalf("accepted malformed archive %s", name)
		}
		s, err = store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		c, _ = s.Connection("synthetic")
		s.Close()
		if c.Name != "Before backup" {
			t.Fatal("rejected restore modified current database")
		}
	}
}
func TestExportContainsOnlyDatabaseAndCredentialFreeManifest(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var buf bytes.Buffer
	if err = Export(context.Background(), s, &buf, "test"); err != nil {
		t.Fatal(err)
	}
	archiveData := append([]byte(nil), buf.Bytes()...)
	gz, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
		if h.Mode != 0600 || (h.Name != "manifest.json" && h.Name != "dashboard.sqlite") {
			t.Fatal("unexpected archive entry")
		}
		if h.Name == "manifest.json" {
			raw, _ := io.ReadAll(tr)
			if !bytes.Contains(raw, []byte(`"credentials_included": false`)) {
				t.Fatal("manifest must state credentials excluded")
			}
		}
	}
	if count != 2 {
		t.Fatal(count)
	}
	archive := filepath.Join(t.TempDir(), "empty.backup.tar.gz")
	if err = os.WriteFile(archive, archiveData, 0600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err = Restore(destination, archive); err != nil {
		t.Fatal(err)
	}
	empty, err := store.Open(filepath.Join(destination, "dashboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	connections, err := empty.Connections()
	if err != nil || len(connections) != 0 {
		t.Fatalf("empty backup did not round-trip: %v", err)
	}
}
func TestServiceDefinitionEscapesPaths(t *testing.T) {
	// Parse/render only. Tests never install a real LaunchAgent or touch Keychain.
	var buf strings.Builder
	// Use the production HTML template engine, including its escaping behavior.
	if err := renderPlist(&buf, "/tmp/a & b/quotes\"", "127.0.0.1:7364", "1234567890"); err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(buf.String()))
	for {
		_, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("invalid service XML: %v\n%s", err, buf.String())
		}
	}
	if !strings.Contains(buf.String(), "a &amp; b") || ServiceLabel("1234567890") == ServiceLabel("other-id") {
		t.Fatal("service names/paths unsafe")
	}
}
