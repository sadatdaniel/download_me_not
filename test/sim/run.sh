#!/bin/sh
# Realistic first install on an "Oracle Cloud VM": Ubuntu 24.04 with systemd and Oracle's default
# firewall, using the real public one-liner and real GitHub releases.
#   sh test/sim/run.sh [amd64|arm64]   (default: both; builds the VM image when missing)
# Steps: install an older release -> blocked by the firewall -> open ports as docs/deploy.md says ->
# panel and API answer -> self-update to the latest release -> reboot -> uninstall.
# Under emulation (arm64 on an x86 PC) iptables cannot reach the kernel, so the firewall steps run
# only where it works (the native pass) and are skipped with a note otherwise.
set -eu
export MSYS_NO_PATHCONV=1
HERE=$(cd "$(dirname "$0")" && (pwd -W 2>/dev/null || pwd)) # pwd -W: Windows path for docker.exe under Git Bash
PORT=18443
step() { printf '\n== %s\n' "$*"; }
ok() { printf '   ok: %s\n' "$*"; }
fail() { printf '   FAIL: %s\n' "$*" >&2; docker exec dmn-sim journalctl -u getaload -n 30 --no-pager >&2 || true; exit 1; }
vm() { docker exec dmn-sim "$@"; }
as_ubuntu() { docker exec -u ubuntu dmn-sim bash -lc "$*"; }
host_health() { curl -sk --max-time 5 "https://127.0.0.1:$PORT/v1/health" | grep -q '"ok"'; }
wait_for() {
	i=0
	while ! eval "$1" >/dev/null 2>&1; do
		i=$((i + 1))
		[ $i -lt "$2" ] || return 1
		sleep 3
	done
}
OPEN_PORTS='iptables -I INPUT -p tcp --dport 5623 -j ACCEPT; iptables -I INPUT -p tcp --dport 42069 -j ACCEPT; iptables -I INPUT -p udp --dport 42069 -j ACCEPT; netfilter-persistent save'

run_pass() {
	ARCH=$1
	docker image inspect "dmn-sim:$ARCH" >/dev/null 2>&1 || docker build -q --platform "linux/$ARCH" -t "dmn-sim:$ARCH" "$HERE" >/dev/null
	docker rm -f dmn-sim >/dev/null 2>&1 || true

	step "[$ARCH] boot the VM (Ubuntu 24.04 with systemd)"
	docker run -d --name dmn-sim --hostname oracle-vm --platform "linux/$ARCH" --privileged --cgroupns=host \
		-v /sys/fs/cgroup:/sys/fs/cgroup:rw -p "127.0.0.1:$PORT:5623" "dmn-sim:$ARCH" >/dev/null
	wait_for 'vm systemctl is-system-running 2>/dev/null | grep -qE "running|degraded"' 60 || fail "systemd did not boot"
	vm rm -f /.dockerenv # a real VM has no Docker marker (old releases refuse self-update when they see it)
	FW=0
	if vm iptables -S INPUT 2>/dev/null | grep -q REJECT; then FW=1; fi
	ok "$(vm uname -m), $(vm sh -c '. /etc/os-release; echo $PRETTY_NAME'), Oracle firewall active: $FW"

	step "install the previous release with the public one-liner"
	as_ubuntu "curl -fsSL https://github.com/sadatdaniel/getaload/releases/latest/download/install.sh | sudo sh -s -- --version v0.1.2" >"$HERE/install.log" 2>&1 || {
		cat "$HERE/install.log"
		fail "install"
	}
	TOKEN=$(grep -A2 'shown once' "$HERE/install.log" | grep -o 'getaload_[A-Za-z0-9_-]*' | head -1)
	[ -n "$TOKEN" ] || fail "no token in the installer output"
	grep -q "sudo getaload connect" "$HERE/install.log" || fail "no connection hint"
	vm systemctl is-active --quiet getaload || fail "service not running"
	vm sh -c 'command -v ffmpeg' >/dev/null || fail "ffmpeg not installed by the installer"
	ok "service active, version $(vm getaload version), token printed once, connection hint shown"

	step "Oracle's firewall blocks the panel until the ports are opened"
	if [ $FW = 1 ]; then
		if host_health; then fail "reachable although the firewall should reject it"; fi
		ok "blocked, as on a fresh Oracle VM"
		vm sh -c "$OPEN_PORTS" >/dev/null 2>&1
		host_health || fail "still blocked after opening the ports"
		ok "panel reachable from outside after the docs/deploy.md iptables commands"
	else
		ok "skipped: iptables does not work under emulation (covered by the native pass)"
		host_health || fail "panel not reachable"
	fi

	step "use it from outside with the token"
	curl -sk --max-time 10 -H "Authorization: Bearer $TOKEN" "https://127.0.0.1:$PORT/v1/jobs" | grep -q '^\[' || fail "API with token"
	curl -sk --max-time 10 "https://127.0.0.1:$PORT/" | grep -q '<div id="app">' || fail "web panel"
	code=$(curl -sk -o /dev/null -w '%{http_code}' "https://127.0.0.1:$PORT/v1/jobs")
	[ "$code" = 401 ] || fail "API without a token answered $code"
	ok "web panel served, API works with the token and refuses without it"
	vm sh -c 'cd /tmp && runuser -u getaload -- env GETALOAD_CONFIG=/etc/getaload/config.yaml HOME=/var/lib/getaload getaload doctor --selftest' | sed 's/^/   /'

	step "self-update to the latest release (signed), restart by systemd, confirm after boot"
	FP=$(vm sh -c 'echo | openssl s_client -connect 127.0.0.1:5623 2>/dev/null | openssl x509 -outform der | sha256sum | cut -d" " -f1')
	as_ubuntu "getaload --url https://127.0.0.1:5623 --fingerprint $FP --token $TOKEN update apply" | sed 's/^/   /' || fail "update apply"
	LATEST=$(curl -fsSL https://api.github.com/repos/sadatdaniel/getaload/releases/latest | grep -o '"tag_name": *"v[^"]*"' | grep -o '[0-9][^"]*')
	wait_for "vm getaload version | grep -qx $LATEST && vm systemctl is-active --quiet getaload" 60 || fail "not running $LATEST after the update"
	wait_for "! vm test -e /var/lib/getaload/update-pending.json" 60 || fail "new version never confirmed itself healthy"
	vm test -e /opt/getaload/bin/getaload.prev || fail "previous binary not kept"
	ok "now $LATEST, confirmed healthy, previous version kept for rollback"

	step "first user on the upgraded server (sudo getaload user add), sign in from outside"
	as_ubuntu "sudo getaload user add Alice --pin 1234" | sed 's/^/   /' || fail "user add"
	vm sh -c 'stat -c %U /var/lib/getaload/getaload.db*' | grep -qv '^getaload$' && fail "sudo left database files not owned by getaload"
	UTOK=$(curl -sk --max-time 10 -H 'Content-Type: application/json' -d '{"name":"alice","pin":"1234","device":"sim"}' \
		"https://127.0.0.1:$PORT/v1/login" | grep -o 'getaload_[A-Za-z0-9_-]*')
	[ -n "$UTOK" ] || fail "sign-in with name and PIN"
	curl -sk --max-time 10 -H "Authorization: Bearer $UTOK" "https://127.0.0.1:$PORT/v1/jobs" | grep -q '^\[' || fail "API as Alice"
	code=$(curl -sk -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "https://127.0.0.1:$PORT/v1/jobs")
	[ "$code" = 403 ] || fail "the installer token still reads downloads after a user exists ($code)"
	ok "Alice signs in; the installer's token no longer reaches anyone's downloads"

	step "reboot the VM"
	docker restart dmn-sim >/dev/null
	wait_for 'vm systemctl is-active --quiet getaload' 60 || fail "service not back after reboot"
	wait_for host_health 30 || fail "service or firewall rules lost after reboot"
	ok "service$([ $FW = 1 ] && echo ' and firewall rules') back after a reboot"

	step "uninstall"
	vm sh -c 'mkdir -p /srv/getaload/downloads && touch /srv/getaload/downloads/keep.me'
	as_ubuntu "sudo getaload uninstall --purge" | sed 's/^/   /'
	if vm systemctl is-active --quiet getaload; then fail "service still running"; fi
	if vm test -e /etc/getaload; then fail "config left after --purge"; fi
	vm test -f /srv/getaload/downloads/keep.me || fail "downloads were deleted"
	ok "removed; downloads kept"

	docker rm -f dmn-sim >/dev/null
	rm -f "$HERE/install.log"
	printf '\n[%s] SIMULATION PASSED\n' "$ARCH"
}

for a in ${1:-amd64 arm64}; do run_pass "$a"; done
