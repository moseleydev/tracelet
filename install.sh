#!/bin/sh
# Installs the latest Tracelet release:
#   curl -fsSL https://raw.githubusercontent.com/moseleydev/tracelet/main/install.sh | sh
# Set TRACELET_VERSION=v0.1.0 for a specific version, and
# TRACELET_INSTALL_DIR to choose where the binary goes.
set -eu

repo="moseleydev/tracelet"
version="${TRACELET_VERSION:-}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) echo "tracelet: unsupported OS $os (on Windows, download the zip from https://github.com/$repo/releases)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "tracelet: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
  [ -n "$version" ] || { echo "tracelet: could not find the latest release" >&2; exit 1; }
fi
plain=${version#v}
archive="tracelet_${plain}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading Tracelet $version for $os/$arch..."
curl -fsSL -o "$tmp/$archive" "$base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

# Refuse to install anything that doesn't match the published checksum.
expected=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then actual=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else actual=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1); fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || { echo "tracelet: checksum mismatch, not installing" >&2; exit 1; }

tar -xzf "$tmp/$archive" -C "$tmp" tracelet
dir="${TRACELET_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
install -m 0755 "$tmp/tracelet" "$dir/tracelet"
echo "Installed $("$dir/tracelet" version) to $dir/tracelet"
case ":$PATH:" in *":$dir:"*) ;; *) echo "Add $dir to your PATH to run it from anywhere." ;; esac
