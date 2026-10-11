#!/bin/sh
# End-to-end test of install.sh in a throwaway container (no network, no systemd):
#   docker run --rm --network none -v "$PWD:/src:ro" -v dmn-dev_dmn-gomod:/go/pkg/mod dmn-dev:latest sh /src/scripts/test-install.sh
# Builds a fake release signed with a throwaway key, then installs, upgrades, uninstalls and purges.
set -eu
fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }

W=$(mktemp -d)
cd /src && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -buildvcs=false -o "$W/dmn" ./cmd/dmn
cd "$W"
tar -czf dmn_0.0.1-test_linux_amd64.tar.gz dmn
sha256sum dmn_0.0.1-test_linux_amd64.tar.gz > checksums.txt
openssl ecparam -name prime256v1 -genkey -noout -out k.pem 2>/dev/null
openssl ec -in k.pem -pubout -out pub.pem 2>/dev/null
openssl dgst -sha256 -sign k.pem -out checksums.txt.sig checksums.txt
mkdir rel && cp dmn_0.0.1-test_linux_amd64.tar.gz checksums.txt checksums.txt.sig rel/
export DMN_INSTALL_PUBKEY="$W/pub.pem"

# Tampered release: signature must fail.
mkdir bad && cp rel/* bad/ && echo "0000  dmn_0.0.1-test_linux_amd64.tar.gz" > bad/checksums.txt
if sh /src/scripts/install.sh --from "$W/bad" --yes >/dev/null 2>&1; then fail "tampered release installed"; fi
[ ! -e /opt/dmn/bin/dmn ] || fail "tampered release left a binary"
ok "tampered release refused"

# The real (test-signed) key is not the release key: without DMN_INSTALL_PUBKEY it must refuse.
if env -u DMN_INSTALL_PUBKEY sh /src/scripts/install.sh --from "$W/rel" --yes >/dev/null 2>&1; then fail "accepted a release signed by another key"; fi
ok "foreign signature refused"

sh /src/scripts/install.sh --from "$W/rel" --yes --port 9443 > out.txt 2>&1 || { cat out.txt; fail "install"; }
[ -x /opt/dmn/bin/dmn ] && [ -L /usr/local/bin/dmn ] || fail "binary not installed"
id dmn >/dev/null || fail "user not created"
grep -q "listen: 0.0.0.0:9443" /etc/dmn/config.yaml || fail "config not written"
grep -q "tls: self-signed" /etc/dmn/config.yaml || fail "tls mode"
[ "$(stat -c %U /etc/dmn/config.yaml)" = dmn ] || fail "config not owned by dmn"
[ "$(stat -c %a /etc/dmn)" = 700 ] || fail "config dir not private"
grep -q "Scan with the app" out.txt || fail "no pairing QR printed"
grep -q "Token" out.txt || fail "no owner token printed"
ok "fresh install"

# Re-run: upgrade/repair keeps the config (and its tokens).
cp /etc/dmn/config.yaml before.yaml
sh /src/scripts/install.sh --from "$W/rel" --yes > out2.txt 2>&1 || { cat out2.txt; fail "re-run"; }
cmp -s before.yaml /etc/dmn/config.yaml || fail "re-run changed the config"
grep -q "existing config kept" out2.txt || fail "re-run message"
ok "re-run keeps config"

# Server starts as the service user and answers.
runuser -u dmn -- env DMN_CONFIG=/etc/dmn/config.yaml /opt/dmn/bin/dmn serve > serve.log 2>&1 &
sleep 2
curl -sk https://127.0.0.1:9443/v1/health | grep -q '"ok"' || { cat serve.log; fail "server not answering"; }
kill %1 2>/dev/null || true
ok "service user runs the server over HTTPS"

mkdir -p /srv/dmn/downloads && touch /srv/dmn/downloads/keep.me
sh /src/scripts/install.sh --uninstall >/dev/null
[ ! -e /opt/dmn ] && [ ! -e /usr/local/bin/dmn ] || fail "uninstall left the binary"
[ -f /etc/dmn/config.yaml ] || fail "uninstall removed config"
sh /src/scripts/install.sh --uninstall --purge >/dev/null
[ ! -e /etc/dmn ] && [ ! -e /var/lib/dmn ] || fail "purge left config or data"
[ -f /srv/dmn/downloads/keep.me ] || fail "purge removed the download folder"
ok "uninstall keeps config; purge removes it but never downloads"
echo "ALL INSTALLER TESTS PASSED"
