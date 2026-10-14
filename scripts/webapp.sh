#!/bin/sh
# Puts a Flutter web build (getaload_app: flutter build web --release --wasm --no-web-resources-cdn) into
# internal/webui/app, where the server embeds it: drops debug symbols and stores the big files gzip-compressed.
#   sh scripts/webapp.sh <path to build/web> [internal/webui/app]
set -eu
src=$1
dst=${2:-internal/webui/app}
[ -f "$src/index.html" ] || { echo "no Flutter web build in $src" >&2; exit 1; }
find "$dst" -mindepth 1 ! -name README.md -exec rm -rf {} +
cp -R "$src"/. "$dst"/
find "$dst" -name '*.symbols' -delete
find "$dst" -type f \( -name '*.wasm' -o -name '*.js' -o -name '*.mjs' -o -name '*.json' -o -name '*.ttf' \
	-o -name '*.otf' -o -name NOTICES \) -size +8k -exec gzip -9 {} \;
du -sh "$dst"
