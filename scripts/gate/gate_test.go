// SPDX-License-Identifier: GPL-3.0-only
package gate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// All Git writes below belong to disposable synthetic repositories, never the checkout.
type fixture struct {
	dir string
	env []string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{dir: filepath.Join(t.TempDir(), "repository with spaces")}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			f.env = append(f.env, entry)
		}
	}
	f.env = append(f.env, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	f.must(t, "", "git", "init", "--template=", "--initial-branch=main")
	for _, path := range []string{"scripts/test-gate.sh", "scripts/install-test-gate.sh", ".githooks/pre-push"} {
		data, err := os.ReadFile(filepath.Join("..", "..", path))
		if err != nil {
			t.Fatal(err)
		}
		f.write(t, path, string(data))
	}
	f.write(t, ".gitignore", "private/\n")
	f.write(t, "marker", "indexed\n")
	f.write(t, "Makefile", "build:\n\t@test \"$$(cat marker)\" = indexed\ncheck:\n\t@test ! -e private\n")
	f.must(t, "", "git", "add", ".")
	return f
}

func (f fixture) write(t *testing.T, path, content string) {
	t.Helper()
	path = filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) command(input, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env, cmd.Stdin = f.dir, f.env, strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (f fixture) must(t *testing.T, input, name string, args ...string) string {
	t.Helper()
	out, err := f.command(input, name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return out
}

func TestStagedGate(t *testing.T) {
	t.Run("unborn index excludes local changes and private files", func(t *testing.T) {
		f := newFixture(t)
		f.write(t, "marker", "unstaged\n")
		f.write(t, "private/data", "synthetic private fixture\n")
		out := f.must(t, "", "bash", "scripts/test-gate.sh")
		if !strings.Contains(out, "passed for staged tree ") {
			t.Fatal(out)
		}
		if _, err := f.command("", "git", "rev-parse", "--verify", "HEAD"); err == nil {
			t.Fatal("gate created a commit")
		}
	})
	t.Run("failed check blocks", func(t *testing.T) {
		f := newFixture(t)
		f.write(t, "Makefile", "build:\n\t@true\ncheck:\n\t@false\n")
		f.must(t, "", "git", "add", "Makefile")
		if out, err := f.command("", "bash", "scripts/test-gate.sh"); err == nil || strings.Contains(out, "passed for staged tree") {
			t.Fatalf("failed check accepted: %v\n%s", err, out)
		}
	})
	t.Run("index changes during checks require a rerun", func(t *testing.T) {
		f := newFixture(t)
		f.env = append(f.env, "FIXTURE_REPO="+f.dir)
		f.write(t, "Makefile", "build:\n\t@true\ncheck:\n\t@printf 'changed\\n' > \"$$FIXTURE_REPO/marker\"\n\t@git -C \"$$FIXTURE_REPO\" add marker\n")
		f.must(t, "", "git", "add", "Makefile")
		if out, err := f.command("", "bash", "scripts/test-gate.sh"); err == nil || !strings.Contains(out, "staged files changed") {
			t.Fatalf("changed index accepted: %v\n%s", err, out)
		}
	})
	t.Run("empty index blocks", func(t *testing.T) {
		f := newFixture(t)
		f.must(t, "", "git", "rm", "-r", "--cached", ".")
		if out, err := f.command("", "bash", "scripts/test-gate.sh"); err == nil || !strings.Contains(out, "index is empty") {
			t.Fatalf("empty index: %v\n%s", err, out)
		}
	})
}

func TestInstallGate(t *testing.T) {
	f := newFixture(t)
	f.must(t, "", "git", "config", "core.hooksPath", "/synthetic/global-hooks")
	f.must(t, "", "bash", "scripts/install-test-gate.sh")
	f.must(t, "", "bash", "scripts/install-test-gate.sh")
	if got := f.must(t, "", "git", "config", "core.hooksPath"); got != "/synthetic/global-hooks" {
		t.Fatal("installer changed existing hook configuration")
	}
	if err := os.Remove(filepath.Join(f.dir, ".git", "hooks", "pre-push")); err != nil {
		t.Fatal(err)
	}
	f.write(t, ".git/hooks/pre-push", "existing hook\n")
	if out, err := f.command("", "bash", "scripts/install-test-gate.sh"); err == nil || !strings.Contains(out, "retained") {
		t.Fatalf("existing hook overwritten: %v\n%s", err, out)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, ".git", "hooks", "pre-push"))
	if err != nil || string(data) != "existing hook\n" {
		t.Fatalf("existing hook changed: %v", err)
	}
}

func TestPrePush(t *testing.T) {
	for _, scenario := range []string{"clean", "wrong ref", "unstaged", "staged", "staged cancellation", "untracked", "failed check", "deletion"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFixture(t)
			tree := f.must(t, "", "git", "write-tree")
			head := f.must(t, "Synthetic fixture commit\n", "git", "commit-tree", tree)
			f.must(t, "", "git", "update-ref", "refs/heads/main", head)
			pushed := head
			switch scenario {
			case "wrong ref":
				pushed = f.must(t, "Other fixture commit\n", "git", "commit-tree", tree, "-p", head)
			case "unstaged", "staged":
				f.write(t, "marker", "changed\n")
				if scenario != "unstaged" {
					f.must(t, "", "git", "add", "marker")
				}
			case "staged cancellation":
				bad := "build:\n\t@false\ncheck:\n\t@true\n"
				f.write(t, "Makefile", bad)
				f.must(t, "", "git", "add", "Makefile")
				tree = f.must(t, "", "git", "write-tree")
				pushed = f.must(t, "Failing HEAD fixture\n", "git", "commit-tree", tree, "-p", head)
				f.must(t, "", "git", "update-ref", "refs/heads/main", pushed)
				f.write(t, "Makefile", "build:\n\t@true\ncheck:\n\t@true\n")
				f.must(t, "", "git", "add", "Makefile")
				f.write(t, "Makefile", bad)
			case "untracked":
				f.write(t, "unexpected", "synthetic\n")
			case "failed check":
				f.write(t, "Makefile", "build:\n\t@true\ncheck:\n\t@false\n")
				f.must(t, "", "git", "add", "Makefile")
				tree = f.must(t, "", "git", "write-tree")
				pushed = f.must(t, "Failing fixture commit\n", "git", "commit-tree", tree, "-p", head)
				f.must(t, "", "git", "update-ref", "refs/heads/main", pushed)
			case "deletion":
				pushed = strings.Repeat("0", len(head))
			}
			out, err := f.command("refs/heads/main "+pushed+" refs/heads/main "+strings.Repeat("0", len(head))+"\n", "bash", ".githooks/pre-push")
			shouldPass := scenario == "clean" || scenario == "deletion"
			if (err == nil) != shouldPass {
				t.Fatalf("pre-push %s: %v\n%s", scenario, err, out)
			}
		})
	}
}
