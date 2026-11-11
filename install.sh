#!/bin/sh
# ccqc installer for Linux and macOS: installs the ccqc binary for the current user.
#
#   From a cc-quant-chat checkout with Go installed: builds from source.
#   Otherwise: downloads the release binary and verifies its SHA-256.
#
#   sh install.sh [--version vX.Y.Z] [--dir DIR] [--source | --release]
#   sh install.sh --uninstall [--dir DIR]
#   curl -fsSL https://raw.githubusercontent.com/PHPCraftdream/cc-quant-chat/main/install.sh | sh
set -eu

REPO="PHPCraftdream/cc-quant-chat"
NAME="ccqc"
DIR="${CCQC_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="latest"
MODE="auto"
ACTION="install"

say() { printf 'ccqc-install: %s\n' "$*"; }
die() {
	printf 'ccqc-install: error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
usage: sh install.sh [options]
  --version TAG   release to download (default: latest)
  --dir DIR       install directory (default: \$CCQC_INSTALL_DIR or ~/.local/bin)
  --source        build from this checkout (needs Go)
  --release       download the release binary even inside a checkout
  --uninstall     remove the binary (chat data is kept)
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
	--version)
		[ $# -ge 2 ] || die "--version needs a value"
		VERSION=$2
		shift 2
		;;
	--dir)
		[ $# -ge 2 ] || die "--dir needs a value"
		DIR=$2
		shift 2
		;;
	--source) MODE=source && shift ;;
	--release) MODE=release && shift ;;
	--uninstall) ACTION=uninstall && shift ;;
	-h | --help) usage && exit 0 ;;
	*) die "unknown option: $1 (see --help)" ;;
	esac
done

target="$DIR/$NAME"

data_dir() {
	case "$(uname -s)" in
	Darwin) printf '%s\n' "$HOME/Library/Application Support/ccqc" ;;
	*) printf '%s\n' "${XDG_CONFIG_HOME:-$HOME/.config}/ccqc" ;;
	esac
}

if [ "$ACTION" = uninstall ]; then
	if [ -e "$target" ]; then
		rm -f "$target"
		say "removed $target"
	else
		say "nothing installed at $target"
	fi
	say "chat data is kept in $(data_dir) (database, folder settings, nick keys)"
	exit 0
fi

# Script directory; empty when piped from curl.
src=""
case "$0" in
*install.sh) src=$(cd "$(dirname "$0")" && pwd) ;;
esac

is_checkout() {
	[ -n "$src" ] && [ -d "$src/cmd/ccqc" ] && grep -q "^module github.com/$REPO\$" "$src/go.mod" 2>/dev/null
}

have() { command -v "$1" >/dev/null 2>&1; }

fetch() {
	if have curl; then
		curl -fsSL -o "$2" "$1" || die "download failed: $1"
	elif have wget; then
		wget -qO "$2" "$1" || die "download failed: $1"
	else
		die "need curl or wget to download"
	fi
}

sha256() {
	if have sha256sum; then
		sha256sum "$1" | awk '{print $1}'
	elif have shasum; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "need sha256sum or shasum to verify the download"
	fi
}

if [ "$MODE" = auto ]; then
	if is_checkout && have go; then MODE=source; else MODE=release; fi
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/ccqc-install.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

case "$MODE" in
source)
	is_checkout || die "--source needs this script inside a cc-quant-chat checkout"
	have go || die "--source needs Go (https://go.dev/dl/)"
	ver=$(git -C "$src" describe --tags --always --dirty 2>/dev/null || echo dev)
	say "building from source ($ver)"
	(cd "$src" && CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X github.com/$REPO/internal/mcp.Version=$ver" -o "$tmp/$NAME" ./cmd/ccqc)
	;;
release)
	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "unsupported OS: $(uname -s) (on Windows use install.cmd or install.ps1)" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "unsupported CPU: $(uname -m)" ;;
	esac
	asset="${NAME}_${os}_${arch}"
	if [ "$VERSION" = latest ]; then
		base="https://github.com/$REPO/releases/latest/download"
	else
		base="https://github.com/$REPO/releases/download/$VERSION"
	fi
	say "downloading $asset ($VERSION)"
	fetch "$base/$asset" "$tmp/$NAME"
	fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"
	want=$(awk -v f="$asset" '$2 == f || $2 == "*" f {print $1}' "$tmp/SHA256SUMS")
	[ -n "$want" ] || die "$asset is not listed in SHA256SUMS"
	[ "$want" = "$(sha256 "$tmp/$NAME")" ] || die "checksum mismatch for $asset"
	;;
esac

chmod 755 "$tmp/$NAME"
mkdir -p "$DIR"
# Rename inside DIR is atomic; a running ccqc keeps its old file until it exits.
cp "$tmp/$NAME" "$DIR/.$NAME.new"
mv -f "$DIR/.$NAME.new" "$target"
say "installed $("$target" version) to $target"

case ":$PATH:" in
*":$DIR:"*) ;;
*) say "$DIR is not in PATH; add it, e.g.: echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile" ;;
esac
