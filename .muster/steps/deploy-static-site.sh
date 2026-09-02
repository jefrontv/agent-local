#!/usr/bin/env bash
# Muster deploy step, runs on the server over the site's SSH session.
#
# Publishes the marketing site as static files: pulls this repository (it is
# public) into a working copy beside the docroot, then mirrors site/public
# (rendered HTML, robots, sitemap), site/dist and site/assets into the docroot.
# The host's own files stay: its .htaccess, ACME directory, cgi-bin, PHP ini.
set -euo pipefail

REPO="https://github.com/jefrontv/agent-local"
SRC="$HOME/.agent-local-src"
DOCROOT="$HOME/${MUSTER_REMOTE_ROOT:-agent-local}"

if [ -d "$SRC/.git" ]; then
	git -C "$SRC" fetch -q --depth 1 origin main
	git -C "$SRC" reset -q --hard origin/main
else
	git clone -q --depth 1 "$REPO" "$SRC"
fi

mkdir -p "$DOCROOT"
rsync -a --delete \
	--exclude .htaccess --exclude .well-known --exclude cgi-bin \
	--exclude php.ini --exclude .user.ini --exclude dist --exclude assets \
	"$SRC/site/public/" "$DOCROOT/"
rsync -a --delete "$SRC/site/dist/" "$DOCROOT/dist/"
rsync -a --delete "$SRC/site/assets/" "$DOCROOT/assets/"

echo "deployed $(git -C "$SRC" rev-parse --short HEAD) to $DOCROOT"
echo "https://${MUSTER_LIVE_DOMAIN:-al.tools.efront.dev}/"
