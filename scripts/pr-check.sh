#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail
export GOTOOLCHAIN=go1.26.10
export GOWORK=off
cd "$(git rev-parse --show-toplevel)"
tree="$(git write-tree)"
check_dir="$(mktemp -d)"
trap 'rm -rf "$check_dir"' EXIT
# Check every indexed public file; never pass local runtime data to a scanner.
# Submodule gitlinks are not files: they are never built or distributed.
git ls-files -s | awk -F '\t' 'substr($1, 1, 6) != "160000" { print $2 }' > "$check_dir/index-files"
git show :scripts/release-files.txt > "$check_dir/manifest"
LC_ALL=C sort "$check_dir/index-files" > "$check_dir/index-sorted"
LC_ALL=C sort "$check_dir/manifest" > "$check_dir/manifest-sorted"
if ! cmp -s "$check_dir/index-sorted" "$check_dir/manifest-sorted"; then
  echo 'Release manifest must list every indexed file exactly once.' >&2
  exit 1
fi
mkdir "$check_dir/source"
git checkout-index --all --prefix="$check_dir/source/"
(
  cd "$check_dir/source"
  shellcheck scripts/*.sh .githooks/pre-push
  go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= .github/workflows/*.yml
  go run github.com/zricethezav/gitleaks/v8@v8.30.1 dir --redact --no-banner .
)
if [[ "$(git write-tree)" != "$tree" ]]; then
  echo 'The staged files changed during PR checks. Rerun the checks.' >&2
  exit 1
fi
echo "PR checks passed for staged tree $tree."
