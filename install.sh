#!/bin/sh
# Install dpm-trace from GitHub Releases and register it with DPM.
#
#   curl -fsSL https://raw.githubusercontent.com/walnuthq/dpm-trace/main/install.sh | sh
#
# Environment:
#   VERSION      release to install, e.g. 0.1.4 (default: latest)
#   INSTALL_DIR  where the binary goes (default: ~/.local/bin)
#   DPM_HOME     DPM home to register into (default: ~/.dpm)
#   NO_REGISTER  set to 1 to skip `install-plugin`
set -eu

REPO=walnuthq/dpm-trace
INSTALL_DIR=${INSTALL_DIR:-$HOME/.local/bin}
DPM_HOME=${DPM_HOME:-$HOME/.dpm}

fail() {
	echo "error: $*" >&2
	exit 1
}

case $(uname -s) in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported OS $(uname -s); download an archive from https://github.com/$REPO/releases" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported architecture $(uname -m)" ;;
esac

if [ -z "${VERSION:-}" ]; then
	# /releases/latest redirects to /releases/tag/v<version>.
	latest=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
		fail "cannot reach GitHub to find the latest release"
	VERSION=${latest##*/}
fi
VERSION=${VERSION#v}

archive="dpm-trace_${VERSION}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/v$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $archive"
curl -fsSL -o "$tmp/$archive" "$base/$archive" || fail "release v$VERSION has no $archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || fail "release v$VERSION has no checksums.txt"

want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$archive is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
fi
[ "$want" = "$got" ] || fail "checksum mismatch for $archive"

tar xzf "$tmp/$archive" -C "$tmp" dpm-trace
mkdir -p "$INSTALL_DIR"
cp "$tmp/dpm-trace" "$INSTALL_DIR/dpm-trace"
chmod 755 "$INSTALL_DIR/dpm-trace"
echo "Installed $("$INSTALL_DIR/dpm-trace" --version) to $INSTALL_DIR/dpm-trace"

# Registration is per SDK version, so register into every installed SDK; otherwise a
# project pinned to another sdk-version fails with `unknown command "trace"`.
if [ "${NO_REGISTER:-}" = 1 ]; then
	echo "Skipped DPM registration (NO_REGISTER=1)."
elif ! ls "$DPM_HOME"/cache/sdk/open-source/*.yaml >/dev/null 2>&1; then
	echo "No Daml SDK in $DPM_HOME, so \`dpm trace\` is not registered yet."
	echo "Install one (dpm install 3.5.1), then run: dpm-trace install-plugin"
else
	for manifest in "$DPM_HOME"/cache/sdk/open-source/*.yaml; do
		sdk=$(basename "$manifest" .yaml)
		"$INSTALL_DIR/dpm-trace" install-plugin --dpm-home "$DPM_HOME" --sdk-version "$sdk" >/dev/null ||
			fail "install-plugin failed for SDK $sdk"
		echo "Registered \`dpm trace\` for SDK $sdk"
	done
fi

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) echo "Add $INSTALL_DIR to PATH to run dpm-trace directly: export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
