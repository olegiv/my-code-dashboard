// SPDX-License-Identifier: GPL-3.0-only
package gate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Build files that must select exactly the go.mod toolchain. Documentation may
// cite earlier versions as history and is reviewed separately.
var toolchainFiles = []string{"Makefile", "Dockerfile", "scripts/pr-check.sh", "scripts/release.sh", "scripts/test-gate.sh", "scripts/docker-check.sh", "scripts/packagecheck/main.go"}

var (
	goDirective = regexp.MustCompile(`(?m)^go (1\.\d+\.\d+)$`)
	// Matches go1.2.3, escaped go1\.2\.3, "Go 1.2.3", golang:1.2.3 and go-version: '1.2.3'.
	goMention    = regexp.MustCompile(`(?:\bgo|\bGo |golang:|go-version: ')(1\\?\.\d+\\?\.\d+)`)
	dockerFrom   = regexp.MustCompile(`(?m)^FROM\s+(?:--platform=\S+\s+)?(\S+)`)
	pinnedDigest = regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)
)

// toolchainDrift lists build-file Go selections that differ from go.mod.
func toolchainDrift(root string) ([]string, error) {
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	directive := goDirective.FindSubmatch(module)
	if directive == nil {
		return nil, errors.New("go.mod has no patch-level go directive")
	}
	want := string(directive[1])
	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		return nil, err
	}
	paths := slices.Clone(toolchainFiles)
	for _, workflow := range workflows {
		paths = append(paths, filepath.ToSlash(strings.TrimPrefix(workflow, root+string(filepath.Separator))))
	}
	var drift []string
	for _, path := range paths {
		text, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			return nil, err
		}
		drift = append(drift, fileDrift(path, text, want)...)
	}
	return drift, nil
}

func fileDrift(path string, text []byte, want string) []string {
	var drift []string
	mentions := goMention.FindAllSubmatch(text, -1)
	workflow := strings.HasPrefix(path, ".github/")
	if len(mentions) == 0 && !workflow {
		drift = append(drift, path+": no Go version selection")
	}
	for _, mention := range mentions {
		if got := strings.ReplaceAll(string(mention[1]), `\`, ""); got != want {
			drift = append(drift, fmt.Sprintf("%s: %s, go.mod requires %s", path, got, want))
		}
	}
	if workflow && bytes.Contains(text, []byte("actions/setup-go")) {
		for _, required := range []string{"GOTOOLCHAIN: go" + want, "go-version: '" + want + "'"} {
			if !bytes.Contains(text, []byte(required)) {
				drift = append(drift, path+": setup-go without "+required)
			}
		}
	}
	return drift
}

// unpinnedImages lists Dockerfile base images without an immutable digest.
func unpinnedImages(dockerfile []byte) []string {
	var unpinned []string
	for _, from := range dockerFrom.FindAllSubmatch(dockerfile, -1) {
		if !pinnedDigest.Match(from[1]) {
			unpinned = append(unpinned, string(from[1]))
		}
	}
	return unpinned
}

func TestToolchainSelectionMatchesGoMod(t *testing.T) {
	drift, err := toolchainDrift(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 0 {
		t.Fatalf("Go toolchain selection drifted from go.mod:\n%s", strings.Join(drift, "\n"))
	}
}

func TestToolchainDriftDetection(t *testing.T) {
	cases := []struct{ name, path, content, want string }{
		{"consistent", "", "", ""},
		{"Dockerfile tag", "Dockerfile", "FROM golang:1.2.4-trixie@sha256:" + strings.Repeat("0", 64) + "\n", "Dockerfile: 1.2.4, go.mod requires 1.2.3"},
		{"escaped script pattern", "scripts/docker-check.sh", `grep -q ': go1\.2\.4$'` + "\n", "scripts/docker-check.sh: 1.2.4"},
		{"prose message", "scripts/docker-check.sh", "fail 'Image binary was not built with Go 1.2.4.'\n", "scripts/docker-check.sh: 1.2.4"},
		{"removed selection", "Makefile", "build:\n", "Makefile: no Go version selection"},
		{"workflow version", ".github/workflows/build.yml", "env:\n  GOTOOLCHAIN: go1.2.3\n      - uses: actions/setup-go@0\n          go-version: '1.2.4'\n", ".github/workflows/build.yml: 1.2.4"},
		{"workflow without toolchain env", ".github/workflows/build.yml", "      - uses: actions/setup-go@0\n          go-version: '1.2.3'\n", "setup-go without GOTOOLCHAIN: go1.2.3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(path, content string) {
				t.Helper()
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("go.mod", "module example.invalid/synthetic\n\ngo 1.2.3\n")
			for _, path := range toolchainFiles {
				write(path, "export GOTOOLCHAIN=go1.2.3\n")
			}
			write(".github/workflows/build.yml", "env:\n  GOTOOLCHAIN: go1.2.3\n      - uses: actions/setup-go@0\n          go-version: '1.2.3'\n")
			write(".github/workflows/review.yml", "name: synthetic workflow without Go\n")
			if c.path != "" {
				write(c.path, c.content)
			}
			drift, err := toolchainDrift(root)
			if err != nil {
				t.Fatal(err)
			}
			report := strings.Join(drift, "\n")
			if c.want == "" && len(drift) != 0 {
				t.Fatalf("consistent fixture reported drift:\n%s", report)
			}
			if c.want != "" && !strings.Contains(report, c.want) {
				t.Fatalf("drift %q not reported:\n%s", c.want, report)
			}
		})
	}
}

func TestDockerBaseImagesUseDigests(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dockerFrom.FindAll(dockerfile, -1)) == 0 {
		t.Fatal("Dockerfile has no base images")
	}
	if unpinned := unpinnedImages(dockerfile); len(unpinned) != 0 {
		t.Fatalf("base images without a sha256 digest: %v", unpinned)
	}
	synthetic := []byte("FROM --platform=$BUILDPLATFORM golang:1.2.3-trixie AS build\nFROM example.invalid/runtime:latest@sha256:" + strings.Repeat("a", 64) + "\nFROM example.invalid/short@sha256:abc\n")
	if got, want := unpinnedImages(synthetic), []string{"golang:1.2.3-trixie", "example.invalid/short@sha256:abc"}; !slices.Equal(got, want) {
		t.Fatalf("unpinned images = %v, want %v", got, want)
	}
}
