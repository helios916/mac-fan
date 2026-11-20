#!/usr/bin/env bash
# Install mac-fan straight from GitHub:
#
#   curl -fsSL https://raw.githubusercontent.com/helios916/mac-fan/main/install.sh | bash
#
# Pass an install directory with:
#
#   curl -fsSL ... | bash -s -- ~/.local/bin
set -euo pipefail

repo="${MAC_FAN_REPO:-helios916/mac-fan}"
ref="${MAC_FAN_REF:-main}"
install_dir="${1:-$HOME/.local/bin}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

url="https://github.com/${repo}/archive/refs/heads/${ref}.tar.gz"
echo "downloading ${repo} (${ref})..."
curl -fsSL "$url" -o "$work/src.tar.gz"
tar -xzf "$work/src.tar.gz" -C "$work"

src="$(find "$work" -maxdepth 1 -type d -name "$(basename "$repo")-*" | head -n 1)"
if [[ -z "$src" ]]; then
	echo "could not find the unpacked source archive" >&2
	exit 1
fi

"$src/build.sh" --install "$install_dir"
