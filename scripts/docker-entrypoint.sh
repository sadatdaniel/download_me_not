#!/bin/sh
# Seeds the helper tools baked into the image into the data volume, once per tool. From then on
# they are managed there: updated daily with checksum checks and rollback, without a new image.
set -e
mkdir -p /data/bin
for f in /opt/dmn-seed/bin/*; do
	[ -e "$f" ] || continue
	[ -e "/data/bin/$(basename "$f")" ] || cp -p "$f" /data/bin/
done
# First start: create the signing key and the owner token (printed once in the log).
if [ "$1" = serve ] && ! grep -q signkey "$DMN_CONFIG" 2>/dev/null; then
	dmn setup
fi
exec dmn "$@"
