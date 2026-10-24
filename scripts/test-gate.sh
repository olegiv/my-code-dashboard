#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
# Check the exact index, including the initial commit, without private local files.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
candidate_tree=$(git write-tree)
if [[ -z $(git ls-files) ]]; then
  echo 'test-gate: the index is empty; stage the intended files first.' >&2
  exit 1
fi
stage_dir=$(mktemp -d "${TMPDIR:-/tmp}/my-code-dashboard-check.XXXXXX")
trap 'rm -rf "$stage_dir"' EXIT
git checkout-index --all --prefix="$stage_dir/"
echo "test-gate: checking staged tree $candidate_tree"
(
  cd "$stage_dir"
  GOTOOLCHAIN=go1.26.10 GOWORK=off make build check
)
if [[ $(git write-tree) != "$candidate_tree" ]]; then
  echo 'test-gate: staged files changed during checks; rerun before committing.' >&2
  exit 1
fi
echo "test-gate: passed for staged tree $candidate_tree"
