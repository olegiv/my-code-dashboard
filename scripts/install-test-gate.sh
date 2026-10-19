#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
repo_root=$(pwd -P)
hooks_dir=$(git rev-parse --git-common-dir)/hooks
mkdir -p "$hooks_dir"
target="$repo_root/.githooks/pre-push"
hook="$hooks_dir/pre-push"
if [[ -e $hook || -L $hook ]]; then
  if [[ $hook -ef $target ]]; then
    echo 'Repository pre-push test gate is already installed.'
    exit 0
  fi
  echo 'Existing repository pre-push hook retained. Chain scripts/test-gate.sh manually.' >&2
  exit 1
fi
ln -s "$target" "$hook"
echo 'Installed repository pre-push test gate; global Git settings were not changed.'
echo 'If core.hooksPath is configured, its pre-push hook must chain to this repository hook.'
