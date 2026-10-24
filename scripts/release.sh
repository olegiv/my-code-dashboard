#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-only
set -euo pipefail
export GOTOOLCHAIN=go1.26.10
# Keep macOS AppleDouble files and extended attributes out of public archives.
export COPYFILE_DISABLE=1
cd "$(dirname "$0")/.."
release_version="${1:-0.2.0-dev}"
if [[ ! "$release_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]]; then
  echo 'Expected a semantic version without the v prefix.' >&2
  exit 1
fi
artifact_dir="$(pwd)/dist/$release_version"
mkdir -p "$artifact_dir"
# Build and package only the explicit public file manifest, even in a checkout
# containing ignored/untracked documents, Go files or credentials.
stage_dir="$(mktemp -d)"
trap 'rm -rf "$stage_dir"' EXIT
source_name="my-code-dashboard-$release_version-source"
source_dir="$stage_dir/$source_name"
mkdir -p "$source_dir"
copied=$'\n'
while IFS= read -r source_path || [[ -n "$source_path" ]]; do
  if [[ ! "$source_path" =~ ^[A-Za-z0-9_./-]+$ || "$source_path" == /* || "$source_path" == -* || "$source_path" == *//* || "$source_path" =~ (^|/)\.\.?(/|$) || ! -f "$source_path" ]]; then
    echo 'Invalid or missing entry in release source manifest.' >&2
    exit 1
  fi
  case "$copied" in
    *$'\n'"$source_path"$'\n'*) echo 'Duplicate entry in release source manifest.' >&2; exit 1 ;;
  esac
  check_path="$source_path"
  while [[ -n "$check_path" ]]; do
    if [[ -L "$check_path" ]]; then
      echo 'Symbolic link in release source manifest.' >&2
      exit 1
    fi
    if [[ "$check_path" != */* ]]; then break; fi
    check_path="${check_path%/*}"
  done
  mkdir -p "$source_dir/$(dirname "$source_path")"
  cp -p "$source_path" "$source_dir/$source_path"
  copied+="$source_path"$'\n'
done < scripts/release-files.txt
if [[ "$copied" == $'\n' ]]; then
  echo 'Empty release source manifest.' >&2
  exit 1
fi
cd "$source_dir"
for release_os in darwin linux; do
  for release_arch in arm64 amd64; do
    archive_name="my-code-dashboard-$release_version-$release_os-$release_arch"
    mkdir -p "$stage_dir/$archive_name"
    CGO_ENABLED=0 GOOS="$release_os" GOARCH="$release_arch" go build \
      -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$release_version" \
      -o "$stage_dir/$archive_name/my-code-dashboard" ./cmd/my-code-dashboard
    cp README.md LICENSE THIRD_PARTY_NOTICES.md "$stage_dir/$archive_name/"
    cp -R docs "$stage_dir/$archive_name/"
    go run ./scripts/notices -binary "$stage_dir/$archive_name/my-code-dashboard" \
      > "$stage_dir/$archive_name/THIRD_PARTY_LICENSES.txt"
    tar --no-xattrs -czf "$artifact_dir/$archive_name.tar.gz" -C "$stage_dir" "$archive_name"
    rm -rf "${stage_dir:?}/${archive_name:?}"
  done
done
# Include dependency source, not just module references, with binary distributions.
go mod vendor -o "$stage_dir/$source_name/vendor"
tar --no-xattrs -czf "$artifact_dir/$source_name.tar.gz" -C "$stage_dir" "$source_name"
rm -rf "$stage_dir"
trap - EXIT
(
  cd "$artifact_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./*.tar.gz > SHA256SUMS
  else
    shasum -a 256 ./*.tar.gz > SHA256SUMS
  fi
)
echo "Prepared archives, corresponding source, and checksums in $artifact_dir (nothing published)."
