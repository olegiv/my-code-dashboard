// SPDX-License-Identifier: GPL-3.0-only
package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisionedFilesRejectUnsafeOrPublicCredentials(t *testing.T) {
	dir := t.TempDir()
	f := Files{Directory: dir}
	path := filepath.Join(dir, "synthetic")
	if err := os.WriteFile(path, []byte(" synthetic-test-only \n"), 0600); err != nil {
		t.Fatal(err)
	}
	if value, err := f.Get("synthetic"); err != nil || value != "synthetic-test-only" {
		t.Fatalf("%s %v", value, err)
	}
	if _, err := f.Get("../synthetic"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get("synthetic"); err == nil {
		t.Fatal("world-readable credential accepted")
	}
	if err := os.Symlink(path, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get("link"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "large"), []byte(strings.Repeat("x", 16385)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get("large"); err == nil {
		t.Fatal("oversized token accepted")
	}
	if f.Set("id", "value") == nil || f.Delete("id") == nil {
		t.Fatal("browser must not mutate provisioned files")
	}
}
