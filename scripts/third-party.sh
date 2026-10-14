#!/bin/sh
# Prints the licences of every Go module linked into getaload, plus the vendored web UI libraries
# (scope §11: every release ships a third-party licences file). Fails if a GPL/AGPL licence
# appears, because the binary must not link GPL code (AGENTS.md rule 3). Used by GoReleaser and CI.
#   sh scripts/third-party.sh > THIRD_PARTY_LICENSES.txt
set -eu
cd "$(dirname "$0")/.."
bad=0
echo "getaload bundles the following third-party software."
echo
go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}}@{{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/getaload | sort -u |
while read -r mod dir; do
	lic=$(ls "$dir" 2>/dev/null | grep -i -E '^(licen[cs]e|copying|notice)' | head -1 || true)
	echo "================================================================================"
	echo "$mod"
	echo "================================================================================"
	if [ -z "$lic" ]; then
		echo "(no licence file in the module root)"
	else
		cat "$dir/$lic"
		if grep -q -E 'GNU (AFFERO )?GENERAL PUBLIC LICENSE' "$dir/$lic" && ! grep -q 'LESSER' "$dir/$lic"; then
			echo "GPL/AGPL licence in $mod" >&2
			echo bad > "${TMPDIR:-/tmp}/dmn-gpl-found"
		fi
	fi
	echo
done
for f in internal/webui/static/vendor/LICENSE-*; do
	echo "================================================================================"
	echo "web UI: $(basename "$f" | sed 's/LICENSE-//') (see internal/webui/static/vendor/README.md)"
	echo "================================================================================"
	cat "$f"
	echo
done
if [ -f "${TMPDIR:-/tmp}/dmn-gpl-found" ]; then
	rm -f "${TMPDIR:-/tmp}/dmn-gpl-found"
	echo "refusing: GPL/AGPL code would be linked (AGENTS.md rule 3)" >&2
	exit 1
fi
