#!/usr/bin/env bash
# agent-local installer — builds or fetches the binary and puts it on PATH.
# Dependencies (php, mariadb, apache, homebrew) are NOT required up front:
# the app detects what's missing at runtime and offers to install it.
set -euo pipefail

BIN_NAME="agent-local"
DEST="${INSTALL_DIR:-$HOME/.local/bin}"
REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"

echo "==> agent-local installer"

# 1. Locate or build the binary
BINARY=""
if [[ -x "$REPO_ROOT/$BIN_NAME" ]]; then
  BINARY="$REPO_ROOT/$BIN_NAME"
  echo "    found prebuilt binary: $BINARY"
elif command -v go >/dev/null 2>&1; then
  echo "    building from source (go $(go version | awk '{print $3}'))…"
  (cd "$REPO_ROOT" && go build -o "$BIN_NAME" .)
  BINARY="$REPO_ROOT/$BIN_NAME"
elif [[ -n "${RELEASE_URL:-}" ]]; then
  echo "    downloading release binary…"
  tmp="$(mktemp)"
  curl -fsSL "$RELEASE_URL" -o "$tmp"
  chmod +x "$tmp"
  BINARY="$tmp"
else
  echo "ERROR: no prebuilt binary, no Go toolchain, no RELEASE_URL."
  echo "Install Go (brew install go) and re-run, or set RELEASE_URL."
  exit 1
fi

# 2. Install into PATH
mkdir -p "$DEST"
cp "$BINARY" "$DEST/$BIN_NAME"
chmod +x "$DEST/$BIN_NAME"
echo "    installed: $DEST/$BIN_NAME"

case ":$PATH:" in
  *":$DEST:"*) ;;
  *) echo "    note: $DEST is not on PATH. Add: export PATH=\"$DEST:\$PATH\"" ;;
esac

# 3. Report readiness (the app itself handles missing deps on demand)
echo
echo "==> dependency check (app will offer to install anything missing):"
command -v brew  >/dev/null 2>&1 && echo "    homebrew: $(brew --version | head -1)" || echo "    homebrew: missing (run: $BIN_NAME install brew)"
command -v php   >/dev/null 2>&1 && echo "    php:      $(php -v | head -1)" || echo "    php:      missing (run: $BIN_NAME install php 8.3)"
echo "    database:   auto-managed MariaDB ($BIN_NAME install mariadb)"
echo
echo "Done. Start with:"
echo "    $BIN_NAME              # TUI"
echo "    $BIN_NAME create mysite"
