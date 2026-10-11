#!/bin/sh
# download_me_not installer (scope §5.11). One command installs, upgrades or repairs; --uninstall removes.
#   curl -fsSL https://github.com/sadatdaniel/download_me_not/releases/latest/download/install.sh | sudo sh
# While the repo is private: gh release download -R sadatdaniel/download_me_not -p '*' -D /tmp/dmn
#                             sudo sh /tmp/dmn/install.sh --from /tmp/dmn
set -eu

REPO=sadatdaniel/download_me_not
PREFIX=/opt/dmn
CONF_DIR=/etc/dmn
DATA_DIR=${DMN_DATA_DIR:-/var/lib/dmn}
DL_DIR=${DMN_DOWNLOADS_DIR:-/srv/dmn/downloads}
PORT=${DMN_PORT:-8080}
DOMAIN=${DMN_DOMAIN:-}
ACCESS=${DMN_ACCESS:-lan} # lan (self-signed), domain (acme), proxy (plain HTTP on localhost)
VERSION=${DMN_VERSION:-latest}
FROM=${DMN_FROM:-}
START=1
UNINSTALL=0
PURGE=0

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
	echo "  --yes                no questions (the installer asks none today)"
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
	--yes) ;;
	--uninstall) UNINSTALL=1 ;;
	--purge) PURGE=1 ;;
	--docker) echo "Docker: use compose.yaml from the repository (docker compose run --rm dmn setup; docker compose up -d)."; exit 0 ;;
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
		systemctl disable --now dmn 2>/dev/null || true
		rm -f /etc/systemd/system/dmn.service
		systemctl daemon-reload 2>/dev/null || true
	fi
	rm -f /usr/local/bin/dmn
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
	TARBALL=$(cd "$FROM" && ls dmn_*_linux_"$ARCH".tar.gz 2>/dev/null | head -1)
	[ -n "$TARBALL" ] || die "no dmn_*_linux_$ARCH.tar.gz in $FROM"
	cp "$FROM/$TARBALL" "$WORK/"
else
	command -v curl >/dev/null 2>&1 || die "curl is required"
	if [ "$VERSION" = latest ]; then
		BASE="https://github.com/$REPO/releases/latest/download"
	else
		BASE="https://github.com/$REPO/releases/download/$VERSION"
	fi
	for f in checksums.txt checksums.txt.sig; do curl -fsSL -o "$WORK/$f" "$BASE/$f" || die "download $f failed"; done
	TARBALL=$(grep -o "dmn_[^ ]*_linux_$ARCH\.tar\.gz" "$WORK/checksums.txt" | head -1)
	[ -n "$TARBALL" ] || die "no build for linux/$ARCH in this release"
	curl -fsSL -o "$WORK/$TARBALL" "$BASE/$TARBALL" || die "download $TARBALL failed"
fi

say "verifying signature and checksum"
KEYFILE=$WORK/release-key.pem
if [ -n "${DMN_INSTALL_PUBKEY:-}" ]; then cp "$DMN_INSTALL_PUBKEY" "$KEYFILE"; else printf '%s\n' "$PUBKEY" >"$KEYFILE"; fi
openssl dgst -sha256 -verify "$KEYFILE" -signature "$WORK/checksums.txt.sig" "$WORK/checksums.txt" >/dev/null 2>&1 ||
	die "checksums.txt signature is not valid: refusing to install"
(cd "$WORK" && grep " $TARBALL\$" checksums.txt | sha256sum -c - >/dev/null) || die "checksum mismatch for $TARBALL"
tar -xzf "$WORK/$TARBALL" -C "$WORK" dmn

if ! id dmn >/dev/null 2>&1; then
	say "creating system user dmn"
	if command -v useradd >/dev/null 2>&1; then
		useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin dmn
	else
		adduser -S -D -H -h "$DATA_DIR" -s /sbin/nologin dmn
	fi
fi
mkdir -p "$PREFIX/bin" "$CONF_DIR" "$DATA_DIR" "$DL_DIR"
# The service user owns the binary so self-update can replace it.
cp "$WORK/dmn" "$PREFIX/bin/dmn.new"
chmod 0755 "$PREFIX/bin/dmn.new"
mv "$PREFIX/bin/dmn.new" "$PREFIX/bin/dmn"
ln -sf "$PREFIX/bin/dmn" /usr/local/bin/dmn
chown dmn "$PREFIX/bin" "$PREFIX/bin/dmn" "$CONF_DIR" "$DATA_DIR" "$DL_DIR"
chmod 0700 "$CONF_DIR" "$DATA_DIR"

as_dmn() {
	if command -v runuser >/dev/null 2>&1; then
		runuser -u dmn -- env DMN_CONFIG="$CONF_DIR/config.yaml" HOME="$DATA_DIR" "$@"
	else
		su -s /bin/sh dmn -c "DMN_CONFIG=$CONF_DIR/config.yaml HOME=$DATA_DIR $*"
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
	as_dmn /usr/local/bin/dmn config set server.listen "$LISTEN"
	as_dmn /usr/local/bin/dmn config set server.tls "$TLS"
	[ -z "$DOMAIN" ] || as_dmn /usr/local/bin/dmn config set server.domain "$DOMAIN"
	as_dmn /usr/local/bin/dmn config set paths.data "$DATA_DIR"
	as_dmn /usr/local/bin/dmn config set paths.downloads "$DL_DIR"
	say "creating the owner token (shown once)"
	as_dmn /usr/local/bin/dmn setup
else
	say "existing config kept (upgrade or repair)"
fi

if has_systemd; then
	cat >/etc/systemd/system/dmn.service <<UNIT
[Unit]
Description=download_me_not
After=network-online.target
Wants=network-online.target

[Service]
User=dmn
Environment=DMN_CONFIG=$CONF_DIR/config.yaml
ExecStart=$PREFIX/bin/dmn serve
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
		systemctl enable dmn
		systemctl restart dmn
		say "service dmn running"
	fi
else
	say "no systemd here: start it with: runuser -u dmn -- env DMN_CONFIG=$CONF_DIR/config.yaml $PREFIX/bin/dmn serve"
fi

if [ "$FRESH" -eq 1 ]; then
	say "scan this with the app to connect:"
	as_dmn /usr/local/bin/dmn pair || true
fi
say "done. Firewall: allow TCP $PORT (and 42069 TCP+UDP for torrents). Remove with: install.sh --uninstall"
