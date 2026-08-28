#!/usr/bin/env bash
# agy-account-rotator installer.
#
#   curl -fsSL https://raw.githubusercontent.com/MiloudiMohamed/agy-account-rotator/main/install.sh | bash
#
# Env overrides:
#   AGY_ROTATOR_VERSION=v0.1.0   pin a version (default: latest release)
#   AGY_ROTATOR_NO_RC=1          do NOT auto-append the PATH export to your shell rc
set -euo pipefail

REPO="${AGY_ROTATOR_REPO:-MiloudiMohamed/agy-account-rotator}"
VERSION="${AGY_ROTATOR_VERSION:-}"

say()  { printf '%s\n' "$*"; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required (please install it first)"; }
need curl; need tar

# --- detect platform -------------------------------------------------------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) die "unsupported architecture: $ARCH" ;;
esac
case "$OS" in
  linux) OS="linux" ;;
  darwin) OS="darwin" ;;
  *) die "unsupported OS: $OS (windows users: download the .zip from GitHub Releases manually)" ;;
esac

# --- resolve version -------------------------------------------------------
if [ -z "$VERSION" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -o '"tag_name": *"[^"]*"' | head -1 | sed 's/.*"\(v[^"]*\)".*/\1/')"
  [ -n "$VERSION" ] || die "could not determine latest release; check https://github.com/$REPO/releases"
fi

ASSET="agy-rotator_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$VERSION/$ASSET"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

say "Downloading $ASSET ..."
curl -fsSL -o "$TMP/$ASSET" "$URL" || die "download failed ($URL)"
tar -xzf "$TMP/$ASSET" -C "$TMP"

BIN_NAME="agy-rotator"
[ "$OS" = "windows" ] && BIN_NAME="agy-rotator.exe"
[ -f "$TMP/$BIN_NAME" ] || die "archive did not contain $BIN_NAME"

DEST="${HOME}/.local/bin"
mkdir -p "$DEST"
install -m 0755 "$TMP/$BIN_NAME" "$DEST/agy-rotator"
say "✓ installed $(DEST="$DEST" "$DEST/agy-rotator" version) -> $DEST/agy-rotator"

case ":$PATH:" in
  *":$DEST:"*) ;;
  *) say "note: $DEST is not on your PATH yet" ;;
esac

# --- wire up the launch shim ----------------------------------------------
if [ "${AGY_ROTATOR_NO_RC:-0}" = "1" ]; then
  "$DEST/agy-rotator" shim install || true
  say ""
  say "Add this to your shell rc to activate rotation:"
  say "  export PATH=\"\$HOME/.agy-rotator/bin:\$PATH\""
else
  "$DEST/agy-rotator" shim install --write-rc || true
fi

# --- install completions & plugin bundle -----------------------------------
"$DEST/agy-rotator" completions install >/dev/null 2>&1 || true
"$DEST/agy-rotator" plugin install >/dev/null 2>&1 || true

say ""
say "Next step — add your Google account(s):"
say "  $DEST/agy-rotator add"
say ""
say "Then use 'agy' as usual; every launch rotates accounts automatically."
