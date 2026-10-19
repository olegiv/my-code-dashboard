// SPDX-License-Identifier: GPL-3.0-only
// Prints notices for a release binary, its linked dependencies and embedded assets.
package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

type module struct {
	Path, Version, Dir string
}

type packageInfo struct {
	Dir      string
	Standard bool
	Module   *module
}

type notice struct {
	Component, Name, Text string
}

func main() {
	binary := flag.String("binary", "", "release executable to inspect")
	flag.Parse()
	if err := generate(*binary, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(binary string, out io.Writer) error {
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return fmt.Errorf("read release build information: %w", err)
	}
	if info.GoVersion != runtime.Version() || info.Path != "github.com/olegiv/my-code-dashboard/cmd/my-code-dashboard" {
		return fmt.Errorf("use the release toolchain and a my-code-dashboard executable")
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] == "" || settings["GOARCH"] == "" || settings["CGO_ENABLED"] != "0" || settings["-tags"] != "" {
		return fmt.Errorf("expected a release binary with CGO disabled and default build tags")
	}
	deps := map[string]string{}
	for _, dep := range info.Deps {
		if dep.Replace != nil {
			return fmt.Errorf("release dependency replacements require a notice review: %s", dep.Path)
		}
		deps[dep.Path] = dep.Version
	}
	root, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil || len(bytes.TrimSpace(root)) == 0 {
		return fmt.Errorf("cannot resolve release toolchain root")
	}
	cmd := exec.Command("go", "list", "-mod=readonly", "-deps", "-json", "./cmd/my-code-dashboard")
	cmd.Env = append(os.Environ(), "GOOS="+settings["GOOS"], "GOARCH="+settings["GOARCH"], "CGO_ENABLED=0")
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("list release packages: %w", err)
	}
	var packages []packageInfo
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for {
		var pkg packageInfo
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		packages = append(packages, pkg)
	}
	notices, err := collect(packages, deps, strings.TrimSpace(string(root)), info.GoVersion)
	if err != nil {
		return err
	}
	for _, name := range []string{"HTMX-LICENSE", "ALPINE-LICENSE", "VUE-LICENSE"} {
		text, err := os.ReadFile(filepath.Join("internal/web/assets/vendor", name))
		if err != nil || len(bytes.TrimSpace(text)) == 0 {
			return fmt.Errorf("missing or empty embedded browser notice: %s", name)
		}
		notices = append(notices, notice{"Embedded browser assets", name, string(text)})
	}
	var result bytes.Buffer
	fmt.Fprintf(&result, "Third-party notices for My Code Dashboard\nTarget: %s/%s; %s\n\n", settings["GOOS"], settings["GOARCH"], info.GoVersion)
	for _, n := range notices {
		fmt.Fprintf(&result, "===== %s / %s =====\n\n%s\n\n", n.Component, n.Name, strings.TrimSpace(n.Text))
	}
	_, err = out.Write(result.Bytes())
	return err
}

// Inspect only package directories and their ancestors, never unrelated subtrees.
func collect(packages []packageInfo, deps map[string]string, goRoot, goVersion string) ([]notice, error) {
	files := map[string]notice{}
	found := map[string]bool{}
	standard := "Go " + goVersion
	text, err := os.ReadFile(filepath.Join(goRoot, "LICENSE"))
	if err != nil || len(bytes.TrimSpace(text)) == 0 {
		return nil, fmt.Errorf("standard library license not found")
	}
	files[standard+"/LICENSE"] = notice{standard, "LICENSE", string(text)}
	for _, pkg := range packages {
		root, component := goRoot, standard
		if !pkg.Standard {
			if pkg.Module == nil {
				continue
			}
			version, linked := deps[pkg.Module.Path]
			if !linked {
				continue
			}
			if version != pkg.Module.Version {
				return nil, fmt.Errorf("binary/module version mismatch: %s", pkg.Module.Path)
			}
			root, component = pkg.Module.Dir, pkg.Module.Path+"@"+version
		}
		rel, err := filepath.Rel(root, pkg.Dir)
		if root == "" || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return nil, fmt.Errorf("package outside dependency source: %s", component)
		}
		for dir := pkg.Dir; ; dir = filepath.Dir(dir) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if !entry.Type().IsRegular() || !isNotice(entry.Name()) {
					continue
				}
				name, err := filepath.Rel(root, filepath.Join(dir, entry.Name()))
				if err != nil {
					return nil, err
				}
				name = filepath.ToSlash(name)
				// This notice applies only to BoringSSL objects, excluded with CGO_ENABLED=0.
				if pkg.Standard && name == "src/crypto/internal/boring/LICENSE" {
					continue
				}
				key := component + "/" + name
				if _, exists := files[key]; exists {
					continue
				}
				text, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil || len(bytes.TrimSpace(text)) == 0 {
					return nil, fmt.Errorf("missing or empty notice: %s", key)
				}
				files[key] = notice{component, name, string(text)}
				found[component] = true
			}
			if dir == root {
				break
			}
		}
	}
	for path, version := range deps {
		if !found[path+"@"+version] {
			return nil, fmt.Errorf("no notices found for linked dependency: %s", path)
		}
	}
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]notice, 0, len(keys))
	for _, key := range keys {
		result = append(result, files[key])
	}
	return result, nil
}

func isNotice(name string) bool {
	name = strings.ToUpper(name)
	// modernc's logo is not included in executable distributions.
	if strings.HasSuffix(name, ".GO") || name == "LICENSE-LOGO" {
		return false
	}
	return strings.HasPrefix(name, "LICENSE") || strings.HasPrefix(name, "COPYING") || strings.HasPrefix(name, "COPYRIGHT") || strings.HasPrefix(name, "NOTICE")
}
