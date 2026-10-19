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
	"path/filepath"
	"strings"
	"testing"

	"github.com/olegiv/my-code-dashboard/internal/model"
	"github.com/olegiv/my-code-dashboard/internal/store"
)

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
	copies, _ := filepath.Glob(filepath.Join(dir, "before-restore-*.sqlite"))
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
