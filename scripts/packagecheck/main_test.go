// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func sourceFixture(t *testing.T, change string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	files := map[string]string{"README.md": "synthetic source", "vendor/modules.txt": "synthetic modules", "vendor/modernc.org/sqlite/LICENSE": "synthetic license", "vendor/github.com/zalando/go-keyring/LICENSE": "synthetic license"}
	switch change {
	case "changed":
		files["README.md"] = "unexpected content"
	case "unlisted":
		files["private.db"] = "synthetic private data"
	case "traversal":
		files["../escape"] = "synthetic"
	case "missing source":
		delete(files, "README.md")
	case "missing license":
		delete(files, "vendor/modernc.org/sqlite/LICENSE")
	case "metadata":
		files["._README.md"] = "synthetic metadata"
	}
	for path, content := range files {
		h := &tar.Header{Name: "release/" + path, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if change == "link" && path == "README.md" {
			h.Typeflag, h.Linkname, h.Size, content = tar.TypeSymlink, "/synthetic", 0, ""
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestSourceArchive(t *testing.T) {
	for _, change := range []string{"", "changed", "unlisted", "traversal", "missing source", "missing license", "metadata", "link"} {
		t.Run(change, func(t *testing.T) {
			err := checkArchive(sourceFixture(t, change), "release", "source", "0.1.0-dev", map[string][]byte{"README.md": []byte("synthetic source")})
			if (change == "") != (err == nil) {
				t.Fatalf("archive check: %v", err)
			}
		})
	}
}

func TestRejectMalformedRelease(t *testing.T) {
	if err := checkRelease("../../escape"); err == nil || !strings.Contains(err.Error(), "invalid release version") {
		t.Fatalf("invalid version accepted: %v", err)
	}
	if err := checkBinary([]byte("not a binary"), nil, "linux-amd64", "0.1.0-dev"); err == nil {
		t.Fatal("invalid binary metadata accepted")
	}
}
