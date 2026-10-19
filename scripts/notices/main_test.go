// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, path, text string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCollectRuntimeNotices(t *testing.T) {
	goRoot, depRoot, unusedRoot := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, goRoot, "LICENSE", "synthetic Go license")
	writeFixture(t, goRoot, "src/vendor/example.org/crypto/LICENSE", "synthetic nested standard-library license")
	writeFixture(t, goRoot, "src/cmd/compiler/LICENSE", "unshipped compiler license")
	writeFixture(t, goRoot, "src/crypto/internal/boring/LICENSE", "unshipped BoringSSL object license")
	writeFixture(t, depRoot, "LICENSE", "synthetic dependency license")
	writeFixture(t, depRoot, "LICENSE-LOGO", "unshipped logo license")
	writeFixture(t, depRoot, "internal/used/NOTICE", "synthetic nested attribution")
	writeFixture(t, depRoot, "testdata/LICENSE", "unshipped test data license")
	writeFixture(t, depRoot, "unused/LICENSE", "unshipped subpackage license")
	writeFixture(t, unusedRoot, "LICENSE", "unlinked module license")
	dep := &module{"example.org/runtime", "v1.0.0", depRoot}
	packages := []packageInfo{
		{Dir: filepath.Join(goRoot, "src/vendor/example.org/crypto"), Standard: true},
		{Dir: filepath.Join(goRoot, "src/crypto/internal/boring"), Standard: true},
		{Dir: filepath.Join(depRoot, "internal/used"), Module: dep},
		{Dir: filepath.Join(depRoot, "internal/used"), Module: dep},
		{Dir: unusedRoot, Module: &module{"example.org/test-only", "v1.0.0", unusedRoot}},
	}
	notices, err := collect(packages, map[string]string{dep.Path: dep.Version}, goRoot, "go-test")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range notices {
		got = append(got, n.Component+"/"+n.Name)
		if strings.Contains(n.Text, "unshipped") || strings.Contains(n.Text, "unlinked") {
			t.Fatalf("irrelevant notice included: %+v", n)
		}
	}
	want := []string{"Go go-test/LICENSE", "Go go-test/src/vendor/example.org/crypto/LICENSE", "example.org/runtime@v1.0.0/LICENSE", "example.org/runtime@v1.0.0/internal/used/NOTICE"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("notices: %v, want %v", got, want)
	}
}

func TestCollectRejectsIncompleteOrMismatchedSource(t *testing.T) {
	for _, scenario := range []string{"missing module license", "empty module license", "missing module", "version mismatch", "package outside module", "missing Go license"} {
		t.Run(scenario, func(t *testing.T) {
			goRoot, depRoot := t.TempDir(), t.TempDir()
			writeFixture(t, goRoot, "LICENSE", "synthetic Go license")
			writeFixture(t, depRoot, "LICENSE", "synthetic dependency license")
			pkg := packageInfo{Dir: depRoot, Module: &module{"example.org/runtime", "v1.0.0", depRoot}}
			packages := []packageInfo{pkg}
			switch scenario {
			case "missing module license":
				if err := os.Remove(filepath.Join(depRoot, "LICENSE")); err != nil {
					t.Fatal(err)
				}
			case "empty module license":
				writeFixture(t, depRoot, "LICENSE", " \n")
			case "missing module":
				packages = nil
			case "version mismatch":
				pkg.Module.Version = "v2.0.0"
			case "package outside module":
				packages[0].Dir = t.TempDir()
			case "missing Go license":
				if err := os.Remove(filepath.Join(goRoot, "LICENSE")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := collect(packages, map[string]string{"example.org/runtime": "v1.0.0"}, goRoot, "go-test"); err == nil {
				t.Fatalf("accepted %s", scenario)
			}
		})
	}
}
