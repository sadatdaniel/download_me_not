# Deploying download_me_not

Two first-class ways to run it: the **native binary** with systemd (VPS, home server, Raspberry Pi)
or **Docker**. Both give the same server, API, web panel and CLI.

> While the repository is private, release downloads and the Docker image need your GitHub login.
> The commands for that case are marked *private repo*.

## 1. Native install (Linux with systemd)

Supported: x86-64, arm64 (Raspberry Pi 4/5 with a 64-bit OS), armv7 (32-bit Pi OS).
Needs `tar`, `sha256sum` and `openssl` (preinstalled on most distributions) and root.

```sh
curl -fsSL https://github.com/sadatdaniel/download_me_not/releases/latest/download/install.sh | sudo sh
```

*Private repo*, on a machine with the GitHub CLI logged in:

```sh
gh release download -R sadatdaniel/download_me_not -p '*' -D /tmp/dmn
sudo sh /tmp/dmn/install.sh --from /tmp/dmn
```

The installer verifies the release signature and checksum, creates a `dmn` system user, installs
`/opt/dmn/bin/dmn` (linked as `/usr/local/bin/dmn`), writes `/etc/dmn/config.yaml`, creates the
**owner token (printed once, keep it)**, starts the `dmn` service and prints a pairing QR code.

Options (or the matching `DMN_*` environment variables):

| Option | Default | Meaning |
|---|---|---|
| `--access lan` | lan | HTTPS with a self-signed certificate on port 8080 (pinned by fingerprint) |
| `--access domain --domain dl.example.com` | | Automatic Let's Encrypt certificate on port 443 |
| `--access proxy` | | Plain HTTP on 127.0.0.1:8080 for a reverse proxy, Cloudflare Tunnel or Tailscale |
| `--port N` | 8080 | Port for `lan` and `proxy` |
| `--downloads-dir DIR` | /srv/dmn/downloads | Where downloads go |
| `--data-dir DIR` | /var/lib/dmn | Database, tools, certificates |
| `--version vX.Y.Z` | latest | A specific release |
| `--no-start` | | Install without starting |

The installer also installs ffmpeg with the system package manager (skip with `--no-ffmpeg`); the
other helper tools (yt-dlp, deno, rclone) are installed and kept up to date by dmn itself.

Re-running the installer upgrades or repairs and keeps your config and tokens.

Service: `systemctl status dmn`, logs: `journalctl -u dmn -f`.

## 2. Docker

```sh
# compose.yaml from this repository
docker compose up -d
docker compose logs dmn          # first start prints the owner token once
docker compose exec dmn dmn pair --url https://<server-ip>:8080   # pairing QR code (the container cannot know your LAN address)
```

*Private repo*: log in once with a GitHub token that has `read:packages`:
`echo <token> | docker login ghcr.io -u <github-user> --password-stdin`.

The image (amd64, arm64, armv7) contains ffmpeg, yt-dlp, rclone and deno. The helper tools are
copied into the `data` volume on the first start and update themselves daily there; update the
server itself by pulling the new image: `docker compose pull && docker compose up -d`.

To use a host folder for downloads, replace the `downloads` volume with e.g.
`/srv/media:/downloads` and `chown 1000 /srv/media` (the container runs as uid 1000).

## 3. First use

1. Open the web panel at `https://<server>:8080/` (or your domain). With the self-signed
   certificate the browser warns once; compare the fingerprint with the one `dmn pair` prints.
2. Sign in with the owner token.
3. Add a link on the **Add** page, or from a shell on the server:
   `export DMN_TOKEN=<token>; dmn add https://example.com/file.iso`.
4. From another computer, the CLI works too:
   `dmn --url https://<server>:8080 --fingerprint <sha256> --token <token> ls`.

On the server, run `dmn` commands with `sudo` (e.g. `sudo dmn pair --name phone`, `sudo dmn doctor`);
they use `/etc/dmn/config.yaml` and keep it owned by the service user.

Tokens: `sudo dmn setup --name <name> --scope read|write|admin` makes more (re-using a name replaces
that token). `sudo dmn pair --name phone` makes a device token and shows the QR code for the app.

## 4. Ports and firewall

| Port | Protocol | For |
|---|---|---|
| 8080 (or `--port`) | TCP | Web panel and API (`lan`) |
| 443, 80 | TCP | Web panel and API, certificate challenges (`domain`) |
| 42069 | TCP and UDP | Torrent peers (optional but much faster when open) |

**Oracle Cloud**: open the ports in the VCN security list (console) *and* on the instance, whose Ubuntu image rejects inbound traffic by default:
`sudo iptables -I INPUT -p tcp --dport 8080 -j ACCEPT; sudo iptables -I INPUT -p tcp --dport 42069 -j ACCEPT; sudo iptables -I INPUT -p udp --dport 42069 -j ACCEPT; sudo netfilter-persistent save`.

Examples: `sudo ufw allow 8080/tcp && sudo ufw allow 42069` or
`sudo firewall-cmd --permanent --add-port=8080/tcp --add-port=42069/tcp --add-port=42069/udp && sudo firewall-cmd --reload`.

Check everything with `dmn doctor` (helper tools) and `dmn doctor --selftest` (download, stream and
torrent, all on the machine itself, no internet).

## 5. Optional setup

- **Google Drive**: create an OAuth client of type *TVs and Limited Input devices* in Google Cloud
  (Drive API enabled), enter its ID and secret under Settings → gdrive, turn gdrive on and press
  **Connect Google Drive**. Only files the app creates are visible to it (`drive.file` scope).
- **Sites that need a login**: export cookies.txt from your browser and import it under Settings →
  Cookies (stored encrypted, sent only over HTTPS).
- **Torrents through a VPN**: set Settings → torrent → *bind* to the VPN interface (e.g. `wg0`);
  torrents stop while it is down (kill switch). Or set a SOCKS5 proxy.
- **Speed limits**: Settings → Speed limits, or `dmn limits --down 10M --up 2M`.

## 6. Updates

- Server: checks daily and shows "update available" (`updates.server: notify`). Apply with
  `dmn update apply` or the button in Settings; it restarts and rolls back by itself if the new
  version fails to start. `auto` installs updates when no download is running.
- Helper tools: updated daily (`updates.tools: auto`), checksum-verified, previous copy kept
  (`dmn tools rollback yt-dlp`).
- While the repository is private, the server needs a GitHub token to see releases:
  `dmn config set updates.token <token>` (sealed automatically on the next start) or `GITHUB_TOKEN`.

## 7. Configuration and files

| Path (native) | Content |
|---|---|
| `/etc/dmn/config.yaml` | Settings (`dmn config show --effective`), tokens are stored hashed |
| `/etc/dmn/secret.key` | Key for encrypted settings and cookies; back it up with the config |
| `/var/lib/dmn` | Database, managed tools, certificates, DHT cache |
| `/srv/dmn/downloads` | Downloads (yours; never removed by uninstall) |

Precedence: command-line flags > `DMN_*` environment variables > config file > defaults.

## 8. Uninstall

```sh
sudo dmn uninstall                 # service and binary; keeps config, data and downloads
sudo dmn uninstall --purge         # also config and data
sudo sh install.sh --uninstall [--purge]   # same, if the binary is gone
docker compose down                # Docker; add -v to delete the volumes
```

The download folder is only deleted with `dmn uninstall --delete-downloads` after typing its path.
