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
**owner token** (printed once; for scripts and server-wide settings), asks for **your name and a 4-digit
PIN** (you become the first user, the owner), starts the `dmn` service and prints **how to connect**: the
address, the certificate fingerprint and the ports to open. `sudo dmn connect` shows that again.
Without a terminal (cloud-init, CI) pass `--user NAME --pin NNNN`, or add the user later with `sudo dmn user add NAME`.

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
| `--user NAME --pin NNNN` | asked | The first user (the owner); `DMN_USER`, `DMN_PIN` work too |
| `--yes` | | No questions (without `--user`, add users later) |

The installer also installs ffmpeg with the system package manager (skip with `--no-ffmpeg`); the
other helper tools (yt-dlp, deno, rclone) are installed and kept up to date by dmn itself.

Re-running the installer upgrades or repairs and keeps your config, users and tokens (it asks nothing).
To start from scratch: `... | sudo sh -s -- --uninstall --purge`, then install again (downloads stay).

Service: `systemctl status dmn`, logs: `journalctl -u dmn -f`.

## 2. Docker

```sh
# compose.yaml from this repository
docker compose up -d
docker compose logs dmn          # first start prints the owner token once
docker compose exec dmn dmn user add NAME    # the first user (the owner); asks for a PIN
docker compose exec dmn dmn connect          # address, fingerprint and the ports to open
```

*Private repo*: log in once with a GitHub token that has `read:packages`:
`echo <token> | docker login ghcr.io -u <github-user> --password-stdin`.

The image (amd64, arm64, armv7) contains ffmpeg, yt-dlp, rclone and deno. The helper tools are
copied into the `data` volume on the first start and update themselves daily there; update the
server itself by pulling the new image: `docker compose pull && docker compose up -d`.

To use a host folder for downloads, replace the `downloads` volume with e.g.
`/srv/media:/downloads` and `chown 1000 /srv/media` (the container runs as uid 1000).

## 3. First use and users

1. **Connect**: in the Android app (Add server) or a browser, open `https://<server>:8080/`. Use the server's
   **public** IP or domain; a `10.x` / `192.168.x` address only works on the same network. With the
   self-signed certificate the app and the browser show its fingerprint once: it must match the one the
   installer or `sudo dmn connect` printed.
2. **Sign in**: tap your name and enter your PIN. Each device gets its own token and stays signed in;
   Settings, Your account lists your devices and signs any of them out.
3. **Add the others**: Settings, **Users**, Add a user (owner only), or `sudo dmn user add NAME` on the
   server. Users are strictly separate: own downloads (`<downloads>/users/<id>/`), files, links and watch
   progress; nobody sees anyone else's, not even the owner. The owner also manages server-wide settings
   (add-ons, Google Drive, updates).
4. **PINs**: change your own in Settings, Your account. Five wrong tries lock a name for a minute,
   doubling up to an hour. Forgotten: the owner resets it (Settings, Users), or on the server
   `sudo dmn user pin NAME` (also unlocks). `sudo dmn user list`, `sudo dmn user remove NAME`.

**CLI**: on the server, run `dmn` with `sudo` (it uses `/etc/dmn/config.yaml` and keeps files owned by the
service user). To use the CLI as a user (here or from another computer), sign it in once:
`dmn --url https://<server>:8080 --fingerprint <sha256> login NAME` prints a token for `DMN_TOKEN`.

**Tokens without a user** (`sudo dmn setup --name <name> --scope read|write|admin`, `sudo dmn pair`) are for
scripts and server-wide settings: once users exist they no longer reach anyone's downloads or files.

## 4. Ports and firewall

| Port | Protocol | For |
|---|---|---|
| 8080 (or `--port`) | TCP | Web panel and API (`lan`) |
| 443, 80 | TCP | Web panel and API, certificate challenges (`domain`) |
| 42069 | TCP and UDP | Torrent peers (optional but much faster when open) |

**Oracle Cloud** needs two steps, because both the cloud network and the instance block ports:

1. **Console**: menu, Networking, **Virtual cloud networks**, pick the compartment and your VCN, tab
   **Subnets**, your subnet (the instance page shows it under *Primary VNIC*), **Default Security List**,
   **Security rules** (or *Ingress Rules*), **Add Ingress Rules**:
   - Stateless off, Source Type CIDR, Source CIDR `0.0.0.0/0`, IP Protocol **TCP**, Destination Port `8080`
   - **+ Another Ingress Rule**: TCP, Destination Port `42069`
   - **+ Another Ingress Rule**: UDP, Destination Port `42069`
   (A network security group on the instance's VNIC works too; rules from both apply.)
2. **On the VM** (rules go above Oracle's REJECT rule and survive reboots):
   `sudo iptables -I INPUT -p tcp --dport 8080 -j ACCEPT && sudo iptables -I INPUT -p tcp --dport 42069 -j ACCEPT && sudo iptables -I INPUT -p udp --dport 42069 -j ACCEPT && sudo netfilter-persistent save`
   Check: `sudo iptables -L INPUT -n --line-numbers | head -8` (the ACCEPT lines come before REJECT).
3. **Test** from the phone's browser: `https://<public IP>:8080/v1/health` shows `{"status":"ok",...}`
   after the certificate warning. If it times out, one of the two steps is missing; also try with the
   phone's VPN off. `sudo ss -ltnp | grep 8080` must show `0.0.0.0:8080`.

Port 8080 is open to the whole internet so the phone works on mobile data; every request still needs a
device token, PINs lock after five wrong tries, and the app pins the certificate.

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
curl -fsSL https://github.com/sadatdaniel/download_me_not/releases/latest/download/install.sh | sudo sh -s -- --uninstall [--purge]   # same, if the binary is gone
docker compose down                # Docker; add -v to delete the volumes
```

The download folder is only deleted with `dmn uninstall --delete-downloads` after typing its path.
