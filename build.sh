#!/usr/bin/env bash
# Build mac-fan from this checkout.
#
#   ./build.sh                  build ./mac-fan
#   ./build.sh --install        build and copy to ~/.local/bin
#   ./build.sh --install DIR    build and copy to DIR
set -euo pipefail

cd "$(dirname "$0")"

usage() {
	cat <<'EOF'
usage: ./build.sh [options]

Builds ./mac-fan for the current Apple Silicon Mac.

options:
  --install [DIR]  install the binary to DIR (default: ~/.local/bin)
  -h, --help       show this help
EOF
}

install_dir=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	--install)
		install_dir="$HOME/.local/bin"
		if [[ $# -gt 1 && "${2:-}" != -* ]]; then
			install_dir="$2"
			shift
		fi
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "unknown option: $1" >&2
		usage >&2
		exit 2
		;;
	esac
	shift
done

if [[ "$(uname -s)" != "Darwin" ]]; then
	echo "mac-fan only runs on macOS." >&2
	exit 1
fi
if [[ "$(uname -m)" != "arm64" ]]; then
	echo "mac-fan requires an Apple Silicon Mac (arm64)." >&2
	exit 1
fi
if ! xcode-select -p >/dev/null 2>&1; then
	echo "Xcode Command Line Tools are required: xcode-select --install" >&2
	exit 1
fi
if ! command -v go >/dev/null 2>&1; then
	echo "Go 1.21+ is required: https://go.dev/dl/" >&2
	exit 1
fi

echo "building mac-fan..."
CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o mac-fan .
codesign --force --sign - mac-fan >/dev/null 2>&1 || true
echo "built ./mac-fan"

if [[ -n "$install_dir" ]]; then
	mkdir -p "$install_dir"
	cp mac-fan "$install_dir/mac-fan"
	echo "installed $install_dir/mac-fan"
	case ":$PATH:" in
	*":$install_dir:"*) ;;
	*) echo "note: $install_dir is not in PATH" ;;
	esac
fi
