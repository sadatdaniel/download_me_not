#!/bin/sh
# End-to-end test of install.sh in a throwaway container (no network, no systemd):
#   docker run --rm --network none -v "$PWD:/src:ro" -v dmn-dev_dmn-gomod:/go/pkg/mod dmn-dev:latest sh /src/scripts/test-install.sh
# Builds a fake release signed with a throwaway key, then installs, upgrades, uninstalls and purges.
set -eu
fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }

W=$(mktemp -d)
cd /src && CGO_ENABLED=0 GOFLAGS=-mod=mod go build -buildvcs=false -o "$W/getaload" ./cmd/getaload
cd "$W"
tar -czf getaload_0.0.1-test_linux_amd64.tar.gz getaload
sha256sum getaload_0.0.1-test_linux_amd64.tar.gz > checksums.txt
openssl ecparam -name prime256v1 -genkey -noout -out k.pem 2>/dev/null
openssl ec -in k.pem -pubout -out pub.pem 2>/dev/null
openssl dgst -sha256 -sign k.pem -out checksums.txt.sig checksums.txt
mkdir rel && cp getaload_0.0.1-test_linux_amd64.tar.gz checksums.txt checksums.txt.sig rel/
export GETALOAD_INSTALL_PUBKEY="$W/pub.pem"

# Tampered release: signature must fail.
mkdir bad && cp rel/* bad/ && echo "0000  getaload_0.0.1-test_linux_amd64.tar.gz" > bad/checksums.txt
if sh /src/scripts/install.sh --from "$W/bad" --yes >/dev/null 2>&1; then fail "tampered release installed"; fi
[ ! -e /opt/getaload/bin/getaload ] || fail "tampered release left a binary"
ok "tampered release refused"

# The real (test-signed) key is not the release key: without GETALOAD_INSTALL_PUBKEY it must refuse.
if env -u GETALOAD_INSTALL_PUBKEY sh /src/scripts/install.sh --from "$W/rel" --yes >/dev/null 2>&1; then fail "accepted a release signed by another key"; fi
ok "foreign signature refused"

sh /src/scripts/install.sh --from "$W/rel" --yes --port 9443 --user Alice --pin 1234 > out.txt 2>&1 || { cat out.txt; fail "install"; }
[ -x /opt/getaload/bin/getaload ] && [ -L /usr/local/bin/getaload ] || fail "binary not installed"
id getaload >/dev/null || fail "user not created"
grep -q "listen: 0.0.0.0:9443" /etc/getaload/config.yaml || fail "config not written"
grep -q "tls: self-signed" /etc/getaload/config.yaml || fail "tls mode"
[ "$(stat -c %U /etc/getaload/config.yaml)" = getaload ] || fail "config not owned by getaload"
[ "$(stat -c %a /etc/getaload)" = 700 ] || fail "config dir not private"
grep -q "How to connect" out.txt || fail "no connection steps printed"
grep -q "installing getaload" out.txt || fail "no version line on a fresh install"
grep -q "Token" out.txt || fail "no command-line token printed"
grep -q "added Alice" out.txt || fail "first user not added"
grep -q "Admin code:" out.txt || fail "no admin code printed"
for t in yt-dlp deno rclone; do grep -q "installing $t" out.txt || fail "$t not installed up front"; done
# Offline the downloads fail, which must not stop the install. Only the tool lines: out.txt also holds a token.
grep "==> installing \(yt-dlp\|deno\|rclone\)" out.txt; grep -B2 "not installed now" out.txt | grep -v getaload_ || true
ok "fresh install"

# Re-run: upgrade/repair keeps the config (and its tokens).
cp /etc/getaload/config.yaml before.yaml
sh /src/scripts/install.sh --from "$W/rel" --yes > out2.txt 2>&1 || { cat out2.txt; fail "re-run"; }
cmp -s before.yaml /etc/getaload/config.yaml || fail "re-run changed the config"
grep -q "existing config kept" out2.txt || fail "re-run message"
grep -q "already installed (repairing)" out2.txt || fail "no version line on a re-run"
ok "re-run keeps config"

# A re-run follows the folders in the config, not the defaults (a server moved over from dmn keeps /srv/dmn/...;
# the old installer then locked the service out of it).
runuser -u getaload -- env GETALOAD_CONFIG=/etc/getaload/config.yaml /opt/getaload/bin/getaload config set paths.downloads /srv/olddl
sh /src/scripts/install.sh --from "$W/rel" --yes > out3.txt 2>&1 || { cat out3.txt; fail "re-run with own folder"; }
[ "$(stat -c %U /srv/olddl)" = getaload ] || fail "re-run ignored the downloads folder in the config"
runuser -u getaload -- env GETALOAD_CONFIG=/etc/getaload/config.yaml /opt/getaload/bin/getaload config set paths.downloads /srv/getaload/downloads
ok "re-run follows the folders in the config"

# Server starts as the service user and answers.
runuser -u getaload -- env GETALOAD_CONFIG=/etc/getaload/config.yaml /opt/getaload/bin/getaload serve > serve.log 2>&1 &
sleep 2
curl -sk https://127.0.0.1:9443/v1/health | grep -q '"ok"' || { cat serve.log; fail "server not answering"; }
curl -sk -d '{"name":"alice","pin":"1234"}' -H 'Content-Type: application/json' https://127.0.0.1:9443/v1/login | grep -q '"token":"getaload_' || fail "first user cannot sign in"
kill %1 2>/dev/null || true
ok "service user runs the server over HTTPS"

mkdir -p /srv/getaload/downloads && touch /srv/getaload/downloads/keep.me
sh /src/scripts/install.sh --uninstall >/dev/null
[ ! -e /opt/getaload ] && [ ! -e /usr/local/bin/getaload ] || fail "uninstall left the binary"
[ -f /etc/getaload/config.yaml ] || fail "uninstall removed config"
sh /src/scripts/install.sh --uninstall --purge >/dev/null
[ ! -e /etc/getaload ] && [ ! -e /var/lib/getaload ] || fail "purge left config or data"
[ -f /srv/getaload/downloads/keep.me ] || fail "purge removed the download folder"
ok "uninstall keeps config; purge removes it but never downloads"

# No terminal and no --yes (cloud-init, CI, a pipe): no questions, no abort, a hint instead.
sh /src/scripts/install.sh --from "$W/rel" --no-start < /dev/null > out3.txt 2>&1 || { cat out3.txt; fail "install without a terminal"; }
grep -q "no user yet" out3.txt || fail "no hint to add a user"
sh /src/scripts/install.sh --uninstall --purge >/dev/null
ok "install without a terminal asks nothing and explains how to add a user"

# An install from before the rename (download_me_not: /etc/dmn, /var/lib/dmn, user dmn, dmn.db) moves over once.
userdel getaload 2>/dev/null || true # left by the steps above; an old server never had it
useradd --system --home-dir /var/lib/dmn --shell /usr/sbin/nologin dmn
mkdir -p /etc/dmn /var/lib/dmn /srv/dmn/downloads && echo film > /srv/dmn/downloads/film.mkv
O="env GETALOAD_CONFIG=/etc/dmn/config.yaml $W/getaload"
$O config set paths.data /var/lib/dmn >/dev/null && $O config set paths.downloads /srv/dmn/downloads >/dev/null
$O config set server.listen 0.0.0.0:9444 >/dev/null && $O setup >/dev/null && $O user add Old --pin 4321 >/dev/null
mv /var/lib/dmn/getaload.db /var/lib/dmn/dmn.db
mkdir -p /opt/dmn/bin && printf '#!/bin/sh\necho 0.1.14\n' > /opt/dmn/bin/dmn && chmod +x /opt/dmn/bin/dmn # the old binary
chown -R dmn /etc/dmn /var/lib/dmn /srv/dmn
sh /src/scripts/install.sh --from "$W/rel" --yes > out4.txt 2>&1 || { cat out4.txt; fail "install over an old download_me_not install"; }
grep -q "moving the download_me_not install" out4.txt || fail "no migration message"
grep -q "updating getaload 0.1.14 → " out4.txt || fail "no old-to-new version line when moving over"
[ ! -e /etc/dmn ] && [ -f /etc/getaload/config.yaml ] || fail "config not moved"
[ -f /var/lib/getaload/getaload.db ] && [ ! -e /var/lib/dmn ] || fail "data not moved"
id getaload >/dev/null && ! id dmn >/dev/null 2>&1 || fail "service user not renamed"
[ -L /usr/local/bin/dmn ] || fail "no dmn alias"
grep -q "existing config kept" out4.txt || fail "migrated install was treated as new"
[ -f /srv/dmn/downloads/film.mkv ] || fail "downloads moved or lost"
runuser -u getaload -- env GETALOAD_CONFIG=/etc/getaload/config.yaml /opt/getaload/bin/getaload serve > serve2.log 2>&1 &
sleep 2
curl -sk -d '{"name":"old","pin":"4321"}' -H 'Content-Type: application/json' https://127.0.0.1:9444/v1/login | grep -q '"token":"getaload_' || { cat serve2.log; fail "the old install's user cannot sign in after the move"; }
kill %1 2>/dev/null || true
sh /src/scripts/install.sh --uninstall --purge >/dev/null
ok "an old download_me_not install moves to getaload with its users and downloads"
echo "ALL INSTALLER TESTS PASSED"
