#!/usr/bin/env bash
# agent-local installer.
#
#   curl -fsSL https://al.tools.efront.dev/install.sh | bash
#   curl -fsSL https://al.tools.efront.dev/install.sh | bash -s -- --setup
#
# Dependencies (PHP, MariaDB, Apache, Homebrew) are NOT required up front: the
# app detects what is missing at runtime and installs it on request.
#
# Three sources, in order: a binary already built next to this script, the
# latest GitHub release, or a local Go toolchain. Override with RELEASE_URL=…
# or VERSION=v0.2.0, and the destination with INSTALL_DIR=…
#
# --setup runs `agent-local setup` after installing: the root allowlist, hosts
# entries, cert trust and the bare-URL alias, in the order that works, verified.
# A flag rather than a prompt because under `curl | bash` stdin is this script,
# so a `read` would consume the rest of it. Setup's own steps are pipe-safe:
# sudo reads /dev/tty and the authorization dialogs are native GUIs.
set -euo pipefail

RUN_SETUP=0
for arg in "$@"; do
  case "$arg" in
  --setup) RUN_SETUP=1 ;;
  -h | --help)
    sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *)
    echo "unknown argument: $arg (try --help)" >&2
    exit 2
    ;;
  esac
done

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
    # A release download is verified against the release's checksums, and a
    # download that cannot be verified is not installed: the binary ends up
    # running as root under the front daemon, so "checksums unreachable" is a
    # reason to stop, not to proceed. RELEASE_URL points at an arbitrary
    # archive by design and is the one path that installs unverified.
    if [[ -n "${TAG:-}" ]]; then
      if ! curl -fsSL "https://github.com/$REPO/releases/download/$TAG/checksums.txt" -o "$WORKDIR/sums.txt" 2>/dev/null; then
        echo "ERROR: could not fetch checksums.txt for $TAG; refusing to install an unverified archive"
        exit 1
      fi
      GOT="$(shasum -a 256 "$WORKDIR/dl.tar.gz" | awk '{print $1}')"
      if grep -q "$GOT" "$WORKDIR/sums.txt"; then
        echo "    checksum ok"
      else
        echo "ERROR: checksum mismatch for $URL"
        exit 1
      fi
    else
      echo "    RELEASE_URL given: installing without checksum verification"
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
  # Same identifier every build: macOS ties Documents/Desktop consent to it,
  # so an update does not have to ask again.
  codesign -f -s - -i local.agent-local "$STAGED" >/dev/null 2>&1 || true
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
if [[ "$RUN_SETUP" == "1" ]]; then
  # Runs the whole first-run sequence: one password prompt, then the allowlist,
  # hosts entries, certs and the bare-URL alias, verified at the end.
  "$DEST/$BIN_NAME" setup
else
  echo "    $BIN_NAME setup           # one-time: root, certs, hosts, bare URLs"
  echo
  echo "    or install and set up in one go:"
  echo "    curl -fsSL https://al.tools.efront.dev/install.sh | bash -s -- --setup"
fi
