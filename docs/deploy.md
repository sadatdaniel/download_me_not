# Deploying getaload

Two first-class ways to run it: the **native binary** with systemd (VPS, home server, Raspberry Pi)
or **Docker**. Both give the same server, API, web panel and CLI.

> While the repository is private, release downloads and the Docker image need your GitHub login.
> The commands for that case are marked *private repo*.

## 1. Native install (Linux with systemd)

Supported: x86-64, arm64 (Raspberry Pi 4/5 with a 64-bit OS), armv7 (32-bit Pi OS).
Needs `tar`, `sha256sum` and `openssl` (preinstalled on most distributions) and root.

```sh
curl -fsSL https://github.com/sadatdaniel/getaload/releases/latest/download/install.sh | sudo sh
```

*Private repo*, on a machine with the GitHub CLI logged in:

```sh
gh release download -R sadatdaniel/getaload -p '*' -D /tmp/getaload
sudo sh /tmp/getaload/install.sh --from /tmp/getaload
```

The installer verifies the release signature and checksum, creates a `getaload` system user, installs
`/opt/getaload/bin/getaload` (linked as `/usr/local/bin/getaload`), writes `/etc/getaload/config.yaml`, creates the
**owner token** (printed once; for scripts and server-wide settings), asks for **your name and a 4-digit
PIN** (you become the first user, the owner), starts the `getaload` service and prints **how to connect**: the
address, the certificate fingerprint and the ports to open. `sudo getaload connect` shows that again.
Without a terminal (cloud-init, CI) pass `--user NAME --pin NNNN`, or add the user later with `sudo getaload user add NAME`.

Options (or the matching `GETALOAD_*` environment variables):

| Option | Default | Meaning |
|---|---|---|
| `--access lan` | lan | HTTPS with a self-signed certificate on port 8080 (pinned by fingerprint) |
| `--access domain --domain dl.example.com` | | Automatic Let's Encrypt certificate on port 443 |
| `--access proxy` | | Plain HTTP on 127.0.0.1:8080 for a reverse proxy, Cloudflare Tunnel or Tailscale |
| `--port N` | 8080 | Port for `lan` and `proxy` |
| `--downloads-dir DIR` | /srv/getaload/downloads | Where downloads go |
| `--data-dir DIR` | /var/lib/getaload | Database, tools, certificates |
| `--version vX.Y.Z` | latest | A specific release |
| `--no-start` | | Install without starting |
| `--user NAME --pin NNNN` | asked | The first user (the owner); `GETALOAD_USER`, `GETALOAD_PIN` work too |
| `--yes` | | No questions (without `--user`, add users later) |

The installer also installs ffmpeg with the system package manager (skip with `--no-ffmpeg`); the
other helper tools (yt-dlp, deno, rclone) are installed and kept up to date by getaload itself.

Re-running the installer upgrades or repairs and keeps your config, users and tokens (it asks nothing).

**Upgrading from download_me_not** (the name before 2026-10-14, command `dmn`): run the one-liner once. It moves
`/etc/dmn`, `/var/lib/dmn`, the `dmn` service user and the service over to `getaload`, keeps your users, sign-ins and
downloads (the download folder stays where it is), and leaves `dmn` as an alias for the `getaload` command. Old
servers cannot update themselves across the rename, so this one step is by hand.
To start from scratch: `... | sudo sh -s -- --uninstall --purge`, then install again (downloads stay).

Service: `systemctl status getaload`, logs: `journalctl -u getaload -f`.

## 2. Docker

```sh
# compose.yaml from this repository
docker compose up -d
docker compose logs getaload          # first start prints the owner token once
docker compose exec getaload getaload user add NAME    # the first user (the owner); asks for a PIN
docker compose exec getaload getaload connect          # address, fingerprint and the ports to open
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
   installer or `sudo getaload connect` printed.
2. **Sign in**: tap your name and enter your PIN. Each device gets its own token and stays signed in;
   Settings, Your account lists your devices and signs any of them out.
3. **Add the others**: Settings, **Users**, Add a user (owner only), or `sudo getaload user add NAME` on the
   server. Users are strictly separate: own downloads (`<downloads>/users/<id>/`), files, links and watch
   progress; nobody sees anyone else's, not even the owner. The owner also manages server-wide settings
   (add-ons, Google Drive, updates).
4. **PINs**: change your own in Settings, Your account. Five wrong tries lock a name for a minute,
   doubling up to an hour. Forgotten: the owner resets it (Settings, Users), or on the server
   `sudo getaload user pin NAME` (also unlocks). `sudo getaload user list`, `sudo getaload user remove NAME`.

**CLI**: on the server, run `getaload` with `sudo` (it uses `/etc/getaload/config.yaml` and keeps files owned by the
service user). To use the CLI as a user (here or from another computer), sign it in once:
`getaload --url https://<server>:8080 --fingerprint <sha256> login NAME` prints a token for `GETALOAD_TOKEN`.

**Tokens without a user** (`sudo getaload setup --name <name> --scope read|write|admin`) are for
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

Check everything with `getaload doctor` (helper tools) and `getaload doctor --selftest` (download, stream and
torrent, all on the machine itself, no internet).

## 5. Optional setup

- **Google Drive**: create an OAuth client of type *TVs and Limited Input devices* in Google Cloud
  (Drive API enabled), enter its ID and secret under Settings → gdrive, turn gdrive on and press
  **Connect Google Drive**. Only files the app creates are visible to it (`drive.file` scope).
- **Sites that need a login** (e.g. YouTube asking a cloud server to "confirm you're not a bot"): export
  cookies.txt from a private browser window signed in to the site (close it right after), and import it in the
  app under Settings, **Site sign-ins**. Each user has their own; only their downloads use them (stored
  encrypted, sent only over HTTPS). Use a spare account.
- **Torrents through a VPN**: set Settings → torrent → *bind* to the VPN interface (e.g. `wg0`);
  torrents stop while it is down (kill switch). Or set a SOCKS5 proxy.
- **Speed limits**: Settings → Speed limits, or `getaload limits --down 10M --up 2M`.

## 6. Updates

- Server: checks daily and shows "update available" (`updates.server: notify`). Apply with
  `getaload update apply` or the button in Settings; it restarts and rolls back by itself if the new
  version fails to start. `auto` installs updates when no download is running.
- Helper tools: updated daily (`updates.tools: auto`), checksum-verified, previous copy kept
  (`getaload tools rollback yt-dlp`).
- While the repository is private, the server needs a GitHub token to see releases:
  `getaload config set updates.token <token>` (sealed automatically on the next start) or `GITHUB_TOKEN`.

## 7. Configuration and files

| Path (native) | Content |
|---|---|
| `/etc/getaload/config.yaml` | Settings (`getaload config show --effective`), tokens are stored hashed |
| `/etc/getaload/secret.key` | Key for encrypted settings; back it up with the config |
| `/var/lib/getaload` | Database, managed tools, certificates, DHT cache |
| `/srv/getaload/downloads` | Downloads (yours; never removed by uninstall) |

Precedence: command-line flags > `GETALOAD_*` environment variables > config file > defaults.

## 8. Uninstall

```sh
sudo getaload uninstall                 # service and binary; keeps config, data and downloads
sudo getaload uninstall --purge         # also config and data
curl -fsSL https://github.com/sadatdaniel/getaload/releases/latest/download/install.sh | sudo sh -s -- --uninstall [--purge]   # same, if the binary is gone
docker compose down                # Docker; add -v to delete the volumes
```

The download folder is only deleted with `getaload uninstall --delete-downloads` after typing its path.
