#!/bin/sh
# Installs piway on a Raspberry Pi running 64-bit Raspberry Pi OS:
#
#   curl -fsSL https://raw.githubusercontent.com/OFK0/piway/main/install.sh | sh
#
# It downloads the latest release, checks its SHA-256 checksum, installs the
# binary to ~/.local/bin/piway and runs `piway setup`, which shows every
# command it runs with sudo before running it. Options go after `sh -s --`:
#
#   ... | sh -s -- --version v0.2.0   install that release instead of the latest
#   ... | sh -s -- --no-setup         install the binary only
#   ... | sh -s -- --dry-run          any other option is passed to piway setup
#
# Tests point it elsewhere with PIWAY_RELEASES, PIWAY_BIN_DIR and PIWAY_ARCH.
set -eu

releases=${PIWAY_RELEASES:-https://github.com/OFK0/piway/releases}
bin_dir=${PIWAY_BIN_DIR:-$HOME/.local/bin}
asset=piway_linux_arm64.tar.gz

say() { printf '%s\n' "$*"; }
die() {
	printf 'piway install: %s\n' "$*" >&2
	exit 1
}

version=latest
run_setup=1
setup_args=
while [ $# -gt 0 ]; do
	case $1 in
	--version)
		[ $# -ge 2 ] || die "--version needs a release, such as v0.2.0"
		version=$2
		shift 2
		;;
	--no-setup)
		run_setup=0
		shift
		;;
	*)
		setup_args="$setup_args $1"
		shift
		;;
	esac
done

os=$(uname -s)
arch=${PIWAY_ARCH:-$(uname -m)}
[ "$os" = Linux ] || die "piway runs on a Raspberry Pi with Linux; this is $os"
case $arch in
aarch64 | arm64) ;;
armv6l | armv7l)
	die "this is 32-bit Raspberry Pi OS ($arch); piway needs the 64-bit one. Flash \"Raspberry Pi OS (64-bit)\" or \"Lite (64-bit)\" with Raspberry Pi Imager."
	;;
*) die "piway runs on a 64-bit Raspberry Pi (arm64); this is $arch" ;;
esac
for tool in curl sha256sum tar; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is needed: sudo apt install $tool"
done

if [ "$version" = latest ]; then
	base=$releases/latest/download
else
	base=$releases/download/$version
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
cd "$tmp"

say "Downloading piway ($version)…"
curl -fsSLO "$base/$asset" || die "could not download $base/$asset"
curl -fsSLO "$base/checksums.txt" || die "could not download $base/checksums.txt"
grep -q " $asset\$" checksums.txt || die "checksums.txt has no entry for $asset; nothing was installed"
sha256sum -c --ignore-missing --quiet checksums.txt >/dev/null 2>&1 ||
	die "checksum mismatch: the download is damaged or not the published release; nothing was installed"

tar -xzf "$asset"
mkdir -p "$bin_dir"
install -m 755 piway_linux_arm64/piway "$bin_dir/piway"
say "Installed $("$bin_dir/piway" -version) at $bin_dir/piway"

cd /
rm -rf "$tmp"
trap - EXIT INT TERM

if [ "$run_setup" = 0 ]; then
	say "Next: $bin_dir/piway setup"
	exit 0
fi
if ! "$bin_dir/piway" setup -h >/dev/null 2>&1; then
	say "This release has no \`piway setup\` yet; install a newer one with --version, or follow docs/RELEASING.md."
	exit 0
fi
# shellcheck disable=SC2086 # setup_args is a list of words
if [ -t 0 ]; then
	exec "$bin_dir/piway" setup $setup_args
elif [ -r /dev/tty ] && (: </dev/tty) 2>/dev/null; then
	# Piped into sh: stdin is this script, so setup asks on the terminal.
	exec "$bin_dir/piway" setup $setup_args </dev/tty
else
	say "No terminal to ask questions on. Run: $bin_dir/piway setup   (or with --yes)"
fi
