#!/usr/bin/env bash
# Install mac-fan straight from GitHub:
#
#   curl -fsSL https://raw.githubusercontent.com/helios916/mac-fan/main/install.sh | bash
#
# Pass an install directory with:
#
#   curl -fsSL ... | bash -s -- ~/.local/bin
#
# Missing build dependencies are handled here: Xcode Command Line Tools are
# triggered through the system installer (one click in the dialog), and a
# portable Go toolchain is downloaded when no Go is found on PATH.
set -euo pipefail

repo="${MAC_FAN_REPO:-helios916/mac-fan}"
ref="${MAC_FAN_REF:-main}"
install_dir="${1:-$HOME/.local/bin}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
	echo "mac-fan only runs on Apple Silicon Macs." >&2
	exit 1
fi

ensure_clt() {
	if xcode-select -p >/dev/null 2>&1; then
		return
	fi
	echo "Xcode Command Line Tools are missing; opening the system installer..."
	xcode-select --install >/dev/null 2>&1 || true
	echo "Click \"Install\" in the dialog, then wait for it to finish."
	for _ in $(seq 1 180); do
		if xcode-select -p >/dev/null 2>&1; then
			echo "Command Line Tools installed."
			return
		fi
		sleep 5
	done
	echo "timed out waiting for Xcode Command Line Tools" >&2
	exit 1
}

ensure_go() {
	if command -v go >/dev/null 2>&1; then
		return
	fi
	toolchain="$HOME/.local/share/mac-fan/toolchain"
	if [[ -x "$toolchain/go/bin/go" ]]; then
		export PATH="$toolchain/go/bin:$PATH"
		return
	fi
	echo "Go is not installed; downloading a portable toolchain..."
	file=""
	for base in "${MAC_FAN_GO_BASE:-https://go.dev}" "https://golang.google.cn"; do
		file="$(curl -fsSL -m 20 "$base/dl/?mode=json" |
			grep -o '"go[0-9.]*\.darwin-arm64\.tar\.gz"' | head -n 1 | tr -d '"')" || file=""
		[[ -n "$file" ]] || continue
		if curl -fsSL "$base/dl/$file" -o "$work/go.tar.gz"; then
			break
		fi
		file=""
	done
	if [[ -z "$file" ]]; then
		echo "could not download the Go toolchain" >&2
		exit 1
	fi
	mkdir -p "$toolchain"
	tar -xzf "$work/go.tar.gz" -C "$toolchain"
	export PATH="$toolchain/go/bin:$PATH"
	echo "installed $(go version)"
}

ensure_clt
ensure_go

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
