#!/usr/bin/env bash
# agent-local installer.
#
#   curl -fsSL https://raw.githubusercontent.com/jefrontv/agent-local/main/install.sh | bash
#
# Dependencies (PHP, MariaDB, Apache, Homebrew) are NOT required up front: the
# app detects what is missing at runtime and installs it on request.
#
# Three sources, in order: a binary already built next to this script, the
# latest GitHub release, or a local Go toolchain. Override with RELEASE_URL=…
# or VERSION=v0.2.0, and the destination with INSTALL_DIR=…
set -euo pipefail

BIN_NAME="agent-local"
REPO="jefrontv/agent-local"
DEST="${INSTALL_DIR:-$HOME/.local/bin}"
REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
ASSET_SUFFIX="darwin_universal.tar.gz"

echo "==> agent-local installer"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "ERROR: agent-local is macOS-only (LaunchDaemons, /etc/hosts, System keychain, Homebrew)."
  exit 1
fi

WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

# 1. Locate a binary: prebuilt beside the script, published release, or source.
BINARY=""
if [[ -x "$REPO_ROOT/$BIN_NAME" && -z "${FORCE_DOWNLOAD:-}" ]]; then
  BINARY="$REPO_ROOT/$BIN_NAME"
  echo "    using prebuilt binary: $BINARY"
else
  URL="${RELEASE_URL:-}"
  if [[ -z "$URL" ]]; then
    TAG="${VERSION:-}"
    if [[ -z "$TAG" ]]; then
      echo "    resolving latest release…"
      TAG="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1 || true)"
    fi
    if [[ -n "$TAG" ]]; then
      VER="${TAG#v}"
      URL="https://github.com/$REPO/releases/download/$TAG/${BIN_NAME}_${VER}_${ASSET_SUFFIX}"
    fi
  fi

  if [[ -n "$URL" ]] && curl -fsSL "$URL" -o "$WORKDIR/dl.tar.gz" 2>/dev/null; then
    echo "    downloaded ${TAG:-release}"
    # Verify against the release checksums when they are reachable; a corrupt
    # download must not become an installed binary.
    if [[ -n "${TAG:-}" ]] &&
      curl -fsSL "https://github.com/$REPO/releases/download/$TAG/checksums.txt" -o "$WORKDIR/sums.txt" 2>/dev/null; then
      GOT="$(shasum -a 256 "$WORKDIR/dl.tar.gz" | awk '{print $1}')"
      if grep -q "$GOT" "$WORKDIR/sums.txt"; then
        echo "    checksum ok"
      else
        echo "ERROR: checksum mismatch for $URL"
        exit 1
      fi
    fi
    tar -xzf "$WORKDIR/dl.tar.gz" -C "$WORKDIR"
    BINARY="$WORKDIR/$BIN_NAME"
    chmod +x "$BINARY"
    # A downloaded file is quarantined by Gatekeeper; the binary is ad-hoc
    # signed, not notarized, so clear the flag or the first run is killed.
    xattr -d com.apple.quarantine "$BINARY" 2>/dev/null || true
  elif command -v go >/dev/null 2>&1 && [[ -f "$REPO_ROOT/go.mod" ]]; then
    echo "    building from source (go $(go version | awk '{print $3}'))…"
    (cd "$REPO_ROOT" && go build -o "$BIN_NAME" .)
    BINARY="$REPO_ROOT/$BIN_NAME"
  else
    echo "ERROR: no release download, no Go toolchain, no prebuilt binary."
    echo "Install Go (brew install go) and re-run inside a checkout, or set RELEASE_URL."
    exit 1
  fi
fi

# 2. Install into PATH.
# Stage beside the destination and rename: overwriting in place keeps the inode,
# and macOS then SIGKILLs the binary (exit 137) because the code signature AMFI
# cached for that inode no longer matches the bytes.
mkdir -p "$DEST"
STAGED="$DEST/.$BIN_NAME.new.$$"
cp "$BINARY" "$STAGED"
chmod +x "$STAGED"
if command -v codesign >/dev/null 2>&1; then
  codesign -f -s - "$STAGED" >/dev/null 2>&1 || true
fi
mv -f "$STAGED" "$DEST/$BIN_NAME"
echo "    installed: $DEST/$BIN_NAME ($("$DEST/$BIN_NAME" version 2>/dev/null || echo unknown))"

case ":$PATH:" in
*":$DEST:"*) ;;
*) echo "    note: $DEST is not on PATH. Add: export PATH=\"$DEST:\$PATH\"" ;;
esac

# 3. Report readiness (the app installs missing dependencies on demand).
echo
echo "==> dependency check:"
command -v brew >/dev/null 2>&1 && echo "    homebrew: $(brew --version | head -1)" || echo "    homebrew: missing (run: $BIN_NAME install brew)"
command -v php >/dev/null 2>&1 && echo "    php:      $(php -v | head -1)" || echo "    php:      missing (run: $BIN_NAME install php 8.3)"
echo "    database: auto-managed MariaDB ($BIN_NAME install mariadb)"
echo
echo "Next:"
echo "    $BIN_NAME doctor --fix    # install anything missing"
echo "    $BIN_NAME sudo            # one-time: no more password prompts"
echo "    $BIN_NAME create mysite"
