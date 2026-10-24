// SPDX-License-Identifier: GPL-3.0-only
package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prFixture(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	script, err := os.ReadFile("../pr-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "scripts/pr-check.sh", string(script))
	f.write(t, ".github/workflows/example.yml", "name: synthetic workflow\n")
	f.write(t, "scripts/release-files.txt", "")
	f.must(t, "", "git", "add", ".")
	f.write(t, "scripts/release-files.txt", f.must(t, "", "git", "ls-files")+"\n")
	f.must(t, "", "git", "add", ".")
	bin := t.TempDir()
	stub := `#!/usr/bin/env bash
set -euo pipefail
test "$(cat marker)" = indexed
test ! -e private
printf '%s\n' "$*" >> "$PR_TRACE"
if [[ "$*" == *"$FAIL_TOOL"* ]]; then exit 1; fi
if [[ "${CHANGE_INDEX:-0}" == 1 ]]; then
  printf 'changed\n' > "$FIXTURE_REPO/marker"
  git -C "$FIXTURE_REPO" add marker
fi
`
	for _, name := range []string{"go", "shellcheck"} {
		if err = os.WriteFile(filepath.Join(bin, name), []byte(stub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	f.env = append(f.env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "PR_TRACE="+filepath.Join(f.dir, "trace"), "FAIL_TOOL=never-match", "FIXTURE_REPO="+f.dir)
	return f
}

func TestPRChecks(t *testing.T) {
	t.Run("all tools inspect exact index without private files or a HEAD", func(t *testing.T) {
		f := prFixture(t)
		f.write(t, "marker", "unstaged\n")
		f.write(t, "private/runtime", "synthetic private data\n")
		out := f.must(t, "", "bash", "scripts/pr-check.sh")
		trace, err := os.ReadFile(filepath.Join(f.dir, "trace"))
		if err != nil || len(strings.Split(strings.TrimSpace(string(trace)), "\n")) != 3 || !strings.Contains(out, "PR checks passed for staged tree") {
			t.Fatalf("incomplete tool execution: %v\n%s\n%s", err, out, trace)
		}
		if _, err = f.command("", "git", "rev-parse", "--verify", "HEAD"); err == nil {
			t.Fatal("PR check created a commit")
		}
	})
	for _, failed := range []string{"scripts/", "actionlint", "gitleaks"} {
		t.Run("failure blocks "+failed, func(t *testing.T) {
			f := prFixture(t)
			f.env = append(f.env, "FAIL_TOOL="+failed)
			if out, err := f.command("", "bash", "scripts/pr-check.sh"); err == nil || strings.Contains(out, "PR checks passed") {
				t.Fatalf("failed tool accepted: %v\n%s", err, out)
			}
		})
	}
	t.Run("manifest drift blocks before tools", func(t *testing.T) {
		f := prFixture(t)
		f.write(t, "unexpected", "synthetic public file\n")
		f.must(t, "", "git", "add", "unexpected")
		if out, err := f.command("", "bash", "scripts/pr-check.sh"); err == nil || !strings.Contains(out, "manifest must list every indexed file") {
			t.Fatalf("manifest drift accepted: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(f.dir, "trace")); !os.IsNotExist(err) {
			t.Fatal("tools ran before manifest validation")
		}
	})
	t.Run("submodule gitlinks need no manifest entry", func(t *testing.T) {
		f := prFixture(t)
		f.must(t, "", "git", "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("a", 40)+",tools/shared")
		out := f.must(t, "", "bash", "scripts/pr-check.sh")
		trace, err := os.ReadFile(filepath.Join(f.dir, "trace"))
		if err != nil || len(strings.Split(strings.TrimSpace(string(trace)), "\n")) != 3 || !strings.Contains(out, "PR checks passed for staged tree") {
			t.Fatalf("gitlink blocked PR checks: %v\n%s\n%s", err, out, trace)
		}
	})
	t.Run(".claude symlinks need no manifest entry", func(t *testing.T) {
		f := prFixture(t)
		link := filepath.Join(f.dir, ".claude", "commands", "tool.md")
		if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../shared/tool.md", link); err != nil {
			t.Fatal(err)
		}
		f.must(t, "", "git", "add", ".claude")
		if out, err := f.command("", "bash", "scripts/pr-check.sh"); err != nil || !strings.Contains(out, "PR checks passed for staged tree") {
			t.Fatalf(".claude symlink blocked PR checks: %v\n%s", err, out)
		}
	})
	t.Run("symlinks elsewhere still need a manifest entry", func(t *testing.T) {
		f := prFixture(t)
		if err := os.Symlink("marker", filepath.Join(f.dir, "linked")); err != nil {
			t.Fatal(err)
		}
		f.must(t, "", "git", "add", "linked")
		if out, err := f.command("", "bash", "scripts/pr-check.sh"); err == nil || !strings.Contains(out, "manifest must list every indexed file") {
			t.Fatalf("unlisted symlink accepted: %v\n%s", err, out)
		}
	})
	t.Run("index changes invalidate passing tools", func(t *testing.T) {
		f := prFixture(t)
		f.env = append(f.env, "CHANGE_INDEX=1")
		if out, err := f.command("", "bash", "scripts/pr-check.sh"); err == nil || !strings.Contains(out, "staged files changed") {
			t.Fatalf("changed index accepted: %v\n%s", err, out)
		}
	})
}
