// SPDX-License-Identifier: GPL-3.0-only
package gate

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func releaseFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	script, err := os.ReadFile("../release.sh")
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "scripts/release.sh", string(script))
	files := []string{"README.md", "LICENSE", "THIRD_PARTY_NOTICES.md", "SECURITY.md", "CONTRIBUTING.md", "AGENTS.md", "CLAUDE.md", "Makefile", "go.mod", "go.sum", ".gitignore", ".gitattributes", ".golangci.yml", ".githooks/pre-push", ".github/example.yml", "cmd/main.go", "internal/example.go", "scripts/release.sh", "scripts/release-files.txt", "docs/public.md"}
	for _, path := range files {
		if path != "scripts/release.sh" {
			f.write(t, path, "synthetic public source\n")
		}
	}
	f.write(t, "scripts/release-files.txt", strings.Join(files, "\n")+"\n")
	f.write(t, "docs/.env", "SYNTHETIC_PRIVATE_DOCUMENT\n")
	f.write(t, "docs/unused.txt", "SYNTHETIC_UNLISTED_DOCUMENT\n")
	f.write(t, "internal/unlisted.go", "SYNTHETIC_UNLISTED_SOURCE\n")
	// Replace Go only within this fixture: no compilers, downloads or providers run.
	bin := t.TempDir()
	goStub := `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  build)
    if [[ -e internal/unlisted.go || -e docs/.env ]]; then
      printf 'private input reached build\n' >> "$RELEASE_TRACE"
    fi
    while [[ $# -gt 0 ]]; do
      if [[ "$1" == -o ]]; then printf 'synthetic binary\n' > "$2"; break; fi
      shift
    done
    ;;
  run) printf 'synthetic generated notices\n' ;;
  mod) mkdir -p "$4" ;;
  *) exit 1 ;;
esac
`
	if err = os.WriteFile(filepath.Join(bin, "go"), []byte(goStub), 0700); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "RELEASE_TRACE="+filepath.Join(f.dir, "trace"))
	return f
}

func TestReleaseUsesOnlyListedSource(t *testing.T) {
	f := releaseFixture(t)
	f.must(t, "", "bash", "scripts/release.sh", "0.1.0-dev")
	archives, err := filepath.Glob(filepath.Join(f.dir, "dist", "0.1.0-dev", "*.tar.gz"))
	if err != nil || len(archives) != 5 {
		t.Fatalf("release archives: %v", err)
	}
	for _, path := range archives {
		entries := releaseEntries(t, path)
		for _, entry := range entries {
			if strings.HasSuffix(entry, "/.env") || strings.HasSuffix(entry, "/unused.txt") || strings.HasSuffix(entry, "/unlisted.go") {
				t.Fatalf("unlisted private file packaged: %s", entry)
			}
		}
		root := strings.TrimSuffix(filepath.Base(path), ".tar.gz")
		if !slices.Contains(entries, root+"/docs/public.md") {
			t.Fatal("listed documentation omitted")
		}
	}
	if _, err = os.Stat(filepath.Join(f.dir, "trace")); !os.IsNotExist(err) {
		t.Fatal("build used unlisted inputs")
	}
}

func TestReleaseRejectsInvalidSourceManifest(t *testing.T) {
	for _, entry := range []string{"../escape", "/absolute", "missing", "docs/public.md\ndocs/public.md", "docs/link"} {
		t.Run(strings.ReplaceAll(entry, "/", "_"), func(t *testing.T) {
			f := releaseFixture(t)
			if err := os.Symlink(filepath.Join(f.dir, "docs", "public.md"), filepath.Join(f.dir, "docs", "link")); err != nil {
				t.Fatal(err)
			}
			f.write(t, "scripts/release-files.txt", entry+"\n")
			if out, err := f.command("", "bash", "scripts/release.sh", "0.1.0-dev"); err == nil || !strings.Contains(out, "source manifest") {
				t.Fatalf("invalid source manifest accepted: %v\n%s", err, out)
			}
		})
	}
}

func releaseEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var entries []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, h.Name)
	}
}
