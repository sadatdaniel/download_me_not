#!/bin/sh
# Runs the isolated ACME test and checks that getaload obtained and serves a certificate from Pebble.
set -eu
cd "$(dirname "$0")"
dc() { docker compose -f compose.yaml "$@"; }
trap 'dc down -v >/dev/null 2>&1' EXIT
dc up -d challtestsrv pebble >/dev/null
# Pebble's own TLS root (for its API) lives in the image; hand it to getaload through a volume.
docker cp "$(dc ps -q pebble)":/test/certs/pebble.minica.pem ./pebble.minica.pem && docker run --rm -v "$PWD:/in:ro" -v dmn-acme-test_pebble-root:/out dmn-dev:latest cp /in/pebble.minica.pem /out/
rm -f pebble.minica.pem
dc up -d getaload >/dev/null
# Wait for issuance, then fetch Pebble's issuing root and verify the served certificate against it.
for i in $(seq 1 60); do
	if dc exec -T getaload sh -c 'curl -sk https://pebble:15000/roots/0 > /tmp/root.pem && curl -s --cacert /tmp/root.pem --resolve dl.test:5001:127.0.0.1 https://dl.test:5001/v1/health' 2>/dev/null | grep -q '"ok"'; then
		echo "ACME OK: getaload got a certificate from Pebble via a real challenge and serves it for dl.test"
		exit 0
	fi
	sleep 2
done
dc logs getaload | tail -30
echo "ACME FAIL" >&2
exit 1
