#!/bin/sh
# getaload installer (scope §5.11). One command installs, upgrades or repairs; --uninstall removes.
#   curl -fsSL https://github.com/sadatdaniel/getaload/releases/latest/download/install.sh | sudo sh
# While the repo is private: gh release download -R sadatdaniel/getaload -p '*' -D /tmp/getaload
#                             sudo sh /tmp/getaload/install.sh --from /tmp/getaload
set -eu

REPO=sadatdaniel/getaload
PREFIX=/opt/getaload
CONF_DIR=/etc/getaload
DATA_DIR=${GETALOAD_DATA_DIR:-/var/lib/getaload}
DL_DIR=${GETALOAD_DOWNLOADS_DIR:-/srv/getaload/downloads}
PORT=${GETALOAD_PORT:-8080}
DOMAIN=${GETALOAD_DOMAIN:-}
ACCESS=${GETALOAD_ACCESS:-lan} # lan (self-signed), domain (acme), proxy (plain HTTP on localhost)
VERSION=${GETALOAD_VERSION:-latest}
FROM=${GETALOAD_FROM:-}
START=1
FFMPEG=1
UNINSTALL=0
PURGE=0
YES=0
FIRST_USER=${GETALOAD_USER:-}
FIRST_PIN=${GETALOAD_PIN:-}

# Release signing key: same as internal/update/release-key.pub (a test keeps them equal).
PUBKEY='-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEb8akqSYMkRpDgMLVfYFvjFPZ0TQp
HujLX8i1XQ9YXn1Oov8sGQYiq/GHLx/X1vx0Z9jsvT4joKhlOgz6y6FXug==
-----END PUBLIC KEY-----'

usage() {
	echo "Usage: install.sh [options]"
	echo "  --data-dir DIR       data folder (default $DATA_DIR)"
	echo "  --downloads-dir DIR  download folder (default $DL_DIR)"
	echo "  --port N             listening port (default $PORT)"
	echo "  --access MODE        lan | domain | proxy (default $ACCESS)"
	echo "  --domain NAME        domain for --access domain (automatic certificates)"
	echo "  --version vX.Y.Z     release to install (default latest)"
	echo "  --from DIR           use release files from DIR instead of downloading"
	echo "  --no-start           install but do not start the service"
	echo "  --no-ffmpeg          do not install ffmpeg with the system package manager"
	echo "  --user NAME          first user (the owner); asked on a fresh install unless --yes"
	echo "  --pin NNNN           that user's 4-digit PIN (or env GETALOAD_PIN); asked when missing"
	echo "  --yes                no questions (without --user: add users later with: getaload user add NAME)"
	echo "  --uninstall          remove service and binary (keeps config and data)"
	echo "  --purge              with --uninstall: also remove config and data (never the download folder)"
}

while [ $# -gt 0 ]; do
	case $1 in
	--data-dir) DATA_DIR=$2; shift ;;
	--downloads-dir) DL_DIR=$2; shift ;;
	--port) PORT=$2; shift ;;
	--access) ACCESS=$2; shift ;;
	--domain) DOMAIN=$2; shift ;;
	--version) VERSION=$2; shift ;;
	--from) FROM=$2; shift ;;
	--no-start) START=0 ;;
	--no-ffmpeg) FFMPEG=0 ;;
	--yes) YES=1 ;;
	--user) FIRST_USER=$2; shift ;;
	--pin) FIRST_PIN=$2; shift ;;
	--uninstall) UNINSTALL=1 ;;
	--purge) PURGE=1 ;;
	--docker) echo "Docker: use compose.yaml from the repository (docker compose run --rm getaload setup; docker compose up -d)."; exit 0 ;;
	-h | --help) usage; exit 0 ;;
	*) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
	esac
	shift
done

say() { printf '==> %s\n' "$*"; }
die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}
has_systemd() { [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; }

[ "$(id -u)" -eq 0 ] || die "run as root (sudo)"

if [ "$UNINSTALL" -eq 1 ]; then
	if has_systemd; then
		systemctl disable --now getaload 2>/dev/null || true
		rm -f /etc/systemd/system/getaload.service
		systemctl daemon-reload 2>/dev/null || true
	fi
	rm -f /usr/local/bin/getaload
	rm -rf "$PREFIX"
	if [ "$PURGE" -eq 1 ]; then
		rm -rf "$CONF_DIR" "$DATA_DIR"
		say "removed service, binary, config and data. The download folder $DL_DIR was kept; delete it yourself if you want."
	else
		say "removed service and binary. Kept $CONF_DIR, $DATA_DIR and $DL_DIR (--purge removes config and data)."
	fi
	exit 0
fi

case $(uname -m) in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
armv7l | armv7*) ARCH=armv7 ;;
*) die "unsupported CPU $(uname -m)" ;;
esac
[ "$(uname -s)" = Linux ] || die "Linux only"
for c in tar sha256sum openssl; do command -v "$c" >/dev/null 2>&1 || die "$c is required"; done

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
if [ -n "$FROM" ]; then
	cp "$FROM/checksums.txt" "$FROM/checksums.txt.sig" "$WORK/" || die "checksums.txt(.sig) missing in $FROM"
	TARBALL=$(cd "$FROM" && ls getaload_*_linux_"$ARCH".tar.gz 2>/dev/null | head -1)
	[ -n "$TARBALL" ] || die "no getaload_*_linux_$ARCH.tar.gz in $FROM"
	cp "$FROM/$TARBALL" "$WORK/"
else
	command -v curl >/dev/null 2>&1 || die "curl is required"
	if [ "$VERSION" = latest ]; then
		BASE="https://github.com/$REPO/releases/latest/download"
	else
		BASE="https://github.com/$REPO/releases/download/$VERSION"
	fi
	for f in checksums.txt checksums.txt.sig; do curl -fsSL -o "$WORK/$f" "$BASE/$f" || die "download $f failed"; done
	TARBALL=$(grep -o "getaload_[^ ]*_linux_$ARCH\.tar\.gz" "$WORK/checksums.txt" | head -1)
	[ -n "$TARBALL" ] || die "no build for linux/$ARCH in this release"
	curl -fsSL -o "$WORK/$TARBALL" "$BASE/$TARBALL" || die "download $TARBALL failed"
fi

say "verifying signature and checksum"
KEYFILE=$WORK/release-key.pem
if [ -n "${GETALOAD_INSTALL_PUBKEY:-}" ]; then cp "$GETALOAD_INSTALL_PUBKEY" "$KEYFILE"; else printf '%s\n' "$PUBKEY" >"$KEYFILE"; fi
openssl dgst -sha256 -verify "$KEYFILE" -signature "$WORK/checksums.txt.sig" "$WORK/checksums.txt" >/dev/null 2>&1 ||
	die "checksums.txt signature is not valid: refusing to install"
(cd "$WORK" && grep " $TARBALL\$" checksums.txt | sha256sum -c - >/dev/null) || die "checksum mismatch for $TARBALL"
tar -xzf "$WORK/$TARBALL" -C "$WORK" getaload

# ffmpeg merges separate video and audio streams (best quality from video sites). The other helper
# tools (yt-dlp, deno, rclone) are installed and updated by getaload itself; ffmpeg comes from the distro.
if [ "$FFMPEG" -eq 1 ] && ! command -v ffmpeg >/dev/null 2>&1; then
	say "installing ffmpeg with the system package manager"
	if command -v apt-get >/dev/null 2>&1; then
		(apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends ffmpeg) >/dev/null 2>&1
	elif command -v dnf >/dev/null 2>&1; then
		dnf install -y -q ffmpeg >/dev/null 2>&1
	elif command -v apk >/dev/null 2>&1; then
		apk add -q ffmpeg >/dev/null 2>&1
	elif command -v pacman >/dev/null 2>&1; then
		pacman -S --noconfirm --needed ffmpeg >/dev/null 2>&1
	elif command -v zypper >/dev/null 2>&1; then
		zypper -q install -y ffmpeg >/dev/null 2>&1
	fi || true
	command -v ffmpeg >/dev/null 2>&1 || say "warning: ffmpeg could not be installed automatically (on RHEL-like systems it needs RPM Fusion). Install it later; downloads still work, but separate video and audio streams cannot be merged."
fi

# Renamed from download_me_not ("dmn"): an old install moves over once, keeping its config, users, sign-ins and
# downloads. Its download folder stays where it is.
MIGRATED=0
if [ -f /etc/dmn/config.yaml ] && [ ! -e "$CONF_DIR/config.yaml" ]; then
	say "moving the download_me_not install to getaload (config, users and downloads are kept)"
	if has_systemd; then
		systemctl disable --now dmn 2>/dev/null || true
		rm -f /etc/systemd/system/dmn.service
		systemctl daemon-reload 2>/dev/null || true
	fi
	# paths.data and paths.downloads from the old config (indented keys under "paths:")
	old() { awk -v k="$1" '/^paths:/{p=1;next} p&&/^[^ ]/{p=0} p&&$1==k":"{print $2}' /etc/dmn/config.yaml | tr -d '"'; }
	OLD_DATA=$(old data)
	OLD_DL=$(old downloads)
	mv /etc/dmn "$CONF_DIR"
	if [ "$OLD_DATA" = /var/lib/dmn ] && [ -d /var/lib/dmn ] && [ ! -e "$DATA_DIR" ]; then
		mv /var/lib/dmn "$DATA_DIR"
		sed -i "s#/var/lib/dmn#$DATA_DIR#" "$CONF_DIR/config.yaml"
	else
		DATA_DIR=${OLD_DATA:-$DATA_DIR}
	fi
	for s in "" -wal -shm; do [ ! -e "$DATA_DIR/dmn.db$s" ] || mv "$DATA_DIR/dmn.db$s" "$DATA_DIR/getaload.db$s"; done
	DL_DIR=${OLD_DL:-$DL_DIR}
	if id dmn >/dev/null 2>&1 && ! id getaload >/dev/null 2>&1; then
		usermod -l getaload -d "$DATA_DIR" dmn
		groupmod -n getaload dmn 2>/dev/null || true
	fi
	rm -rf /opt/dmn
	rm -f /usr/local/bin/dmn
	MIGRATED=1
fi

if ! id getaload >/dev/null 2>&1; then
	say "creating system user getaload"
	if command -v useradd >/dev/null 2>&1; then
		useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin getaload
	else
		adduser -S -D -H -h "$DATA_DIR" -s /sbin/nologin getaload
	fi
fi
mkdir -p "$PREFIX/bin" "$CONF_DIR" "$DATA_DIR" "$DL_DIR"
# The service user owns the binary so self-update can replace it.
cp "$WORK/getaload" "$PREFIX/bin/getaload.new"
chmod 0755 "$PREFIX/bin/getaload.new"
mv "$PREFIX/bin/getaload.new" "$PREFIX/bin/getaload"
ln -sf "$PREFIX/bin/getaload" /usr/local/bin/getaload
[ "$MIGRATED" -eq 0 ] || ln -sf "$PREFIX/bin/getaload" /usr/local/bin/dmn # the old command keeps working
chown getaload "$PREFIX/bin" "$PREFIX/bin/getaload" "$CONF_DIR" "$DATA_DIR" "$DL_DIR"
chmod 0700 "$CONF_DIR" "$DATA_DIR"

as_svc() {
	if command -v runuser >/dev/null 2>&1; then
		runuser -u getaload -- env GETALOAD_CONFIG="$CONF_DIR/config.yaml" HOME="$DATA_DIR" "$@"
	else
		su -s /bin/sh getaload -c "GETALOAD_CONFIG=$CONF_DIR/config.yaml HOME=$DATA_DIR $*"
	fi
}

FRESH=0
[ -f "$CONF_DIR/config.yaml" ] || FRESH=1
if [ "$FRESH" -eq 1 ]; then
	say "writing $CONF_DIR/config.yaml"
	case $ACCESS in
	lan) LISTEN=0.0.0.0:$PORT TLS=self-signed ;;
	domain)
		[ -n "$DOMAIN" ] || die "--access domain needs --domain"
		LISTEN=0.0.0.0:443 TLS=acme
		;;
	proxy) LISTEN=127.0.0.1:$PORT TLS=off ;;
	*) die "--access must be lan, domain or proxy" ;;
	esac
	as_svc /usr/local/bin/getaload config set server.listen "$LISTEN"
	as_svc /usr/local/bin/getaload config set server.tls "$TLS"
	[ -z "$DOMAIN" ] || as_svc /usr/local/bin/getaload config set server.domain "$DOMAIN"
	as_svc /usr/local/bin/getaload config set paths.data "$DATA_DIR"
	as_svc /usr/local/bin/getaload config set paths.downloads "$DL_DIR"
	say "creating the owner token (shown once)"
	as_svc /usr/local/bin/getaload setup
else
	say "existing config kept (upgrade or repair)"
fi

# The first user (the owner). Questions go to /dev/tty: with `curl | sudo sh`, stdin is the script.
# A subshell: in POSIX sh a failed redirection on a special builtin would end the whole script.
has_tty() { [ "$YES" -eq 0 ] && (: </dev/tty) 2>/dev/null; }
if [ "$FRESH" -eq 1 ] && [ -z "$FIRST_USER" ] && has_tty; then
	printf 'Your name (the first user, who can add the others): ' >/dev/tty
	read -r FIRST_USER </dev/tty
fi
if [ -n "$FIRST_USER" ]; then
	while :; do
		case $FIRST_PIN in [0-9][0-9][0-9][0-9]) break ;; esac
		has_tty || die "--pin must be 4 digits"
		printf 'PIN for %s (4 digits): ' "$FIRST_USER" >/dev/tty
		stty -echo </dev/tty 2>/dev/null || true
		read -r FIRST_PIN </dev/tty
		stty echo </dev/tty 2>/dev/null || true
		echo >/dev/tty
	done
	as_svc env GETALOAD_PIN="$FIRST_PIN" /usr/local/bin/getaload user add "$FIRST_USER" || say "warning: could not add $FIRST_USER (see above); add users later with: sudo getaload user add NAME"
elif [ "$FRESH" -eq 1 ]; then
	say "no user yet: add the first one (the owner) with: sudo getaload user add NAME"
fi

if has_systemd; then
	cat >/etc/systemd/system/getaload.service <<UNIT
[Unit]
Description=getaload
After=network-online.target
Wants=network-online.target

[Service]
User=getaload
Environment=GETALOAD_CONFIG=$CONF_DIR/config.yaml
ExecStart=$PREFIX/bin/getaload serve
# Exits after a self-update or a rollback; systemd starts the right binary.
Restart=always
RestartSec=3
AmbientCapabilities=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$PREFIX $CONF_DIR $DATA_DIR $DL_DIR

[Install]
WantedBy=multi-user.target
UNIT
	systemctl daemon-reload
	if [ "$START" -eq 1 ]; then
		systemctl enable getaload
		systemctl restart getaload
		say "service getaload running"
	fi
else
	say "no systemd here: start it with: runuser -u getaload -- env GETALOAD_CONFIG=$CONF_DIR/config.yaml $PREFIX/bin/getaload serve"
fi

if [ "$FRESH" -eq 1 ]; then
	as_svc /usr/local/bin/getaload connect || say "open https://<this server>:$PORT in the app or a browser"
fi
say "done. Show the connection steps again: sudo getaload connect. Remove: the same one-liner with: | sudo sh -s -- --uninstall"
