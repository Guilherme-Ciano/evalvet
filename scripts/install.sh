#!/usr/bin/env bash
set -euo pipefail

repo="Guilherme-Ciano/evalvet"
version="${EVALVET_VERSION:-latest}"

case "${RUNNER_OS:-$(uname -s)}" in
  Linux) os=linux ;;
  macOS|Darwin) os=darwin ;;
  *) echo "unsupported OS: ${RUNNER_OS:-$(uname -s)}" >&2; exit 1 ;;
esac
case "${RUNNER_ARCH:-$(uname -m)}" in
  X64|x86_64|amd64) arch=amd64 ;;
  ARM64|arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported arch: ${RUNNER_ARCH:-$(uname -m)}" >&2; exit 1 ;;
esac

if [ "$version" = "latest" ]; then
  url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")
  version="${url##*/}"
fi
num="${version#v}"
archive="evalvet_${num}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"
dir="${RUNNER_TEMP:-/tmp}/evalvet-$num"
mkdir -p "$dir"

curl -fsSL --retry 3 -o "$dir/$archive" "$base/$archive"
curl -fsSL --retry 3 -o "$dir/checksums.txt" "$base/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$dir/checksums.txt")
if [ -z "$expected" ]; then
  echo "no checksum for $archive in checksums.txt" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$dir/$archive" | awk '{ print $1 }')
else
  actual=$(shasum -a 256 "$dir/$archive" | awk '{ print $1 }')
fi
if [ "$expected" != "$actual" ]; then
  echo "sha256 mismatch for $archive: expected $expected, got $actual" >&2
  exit 1
fi

tar -xzf "$dir/$archive" -C "$dir" evalvet
chmod +x "$dir/evalvet"
echo "$dir" >> "${GITHUB_PATH:-/dev/null}"
"$dir/evalvet" version
