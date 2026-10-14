# getaload

A self-hosted download server: send it a web link, a video page, a magnet link or a .torrent, it
downloads on your server, and you browse, stream or copy the results to Google Drive from the web
panel, the CLI or the Android app. Several people can share one server, each with their own name, PIN and files. One static Go binary, or a Docker image with every
helper tool included.

- **Web downloads**, IDM-style: up to 16 connections with work stealing, resume, retry (faster than aria2c with slow routes; see [docs/benchmark.md](docs/benchmark.md))
- **Video pages** through yt-dlp, with format and quality choice
- **Torrents**: stream while downloading, seeding rules, peers and piece map, blocklists, SOCKS5, kill switch, swarm memory
- **File manager** over the API: browse, play with seeking, ZIP folders, copy or move to Google Drive
- **Safe defaults**: token scopes, HTTPS (self-signed with pinning, or automatic certificates), SSRF protection, encrypted secrets, signed self-updates with automatic rollback

## Quick start

See **[docs/deploy.md](docs/deploy.md)** for the full guide (Docker, domains, every option).

**1. Install** on a Linux server (systemd, x86-64 / arm64 / armv7):

```sh
curl -fsSL https://github.com/sadatdaniel/getaload/releases/latest/download/install.sh | sudo sh
```

The installer asks for **your name and a 4-digit PIN**: you become the first user (the owner). At the
end it prints how to connect: the address, the certificate fingerprint and the ports to open.
`sudo getaload connect` shows that again at any time.

**2. Open the ports**: TCP **5623** (app and web panel) and TCP+UDP **42069** (torrents, optional but
faster), in the server's firewall *and*, on a cloud VM, in the provider's console. On **Oracle Cloud**:

- Console: Networking, Virtual cloud networks, your VCN, Subnets, your subnet, the Default Security
  List, **Add Ingress Rules**: source `0.0.0.0/0`, protocol TCP, destination port `5623`; add two more
  rules for TCP `42069` and UDP `42069`.
- On the VM (Oracle's Ubuntu image blocks ports itself):
  `sudo iptables -I INPUT -p tcp --dport 5623 -j ACCEPT && sudo iptables -I INPUT -p tcp --dport 42069 -j ACCEPT && sudo iptables -I INPUT -p udp --dport 42069 -j ACCEPT && sudo netfilter-persistent save`

**3. Connect**: in the app (or a browser), enter `https://<server's public IP>:5623`, accept the
certificate after comparing its fingerprint, tap your name and enter your PIN. The device stays
signed in. Use the public IP: a `10.x` or `192.168.x` address only works on the same network.

**4. Add the others**: in the app, Settings, **Users**, Add a user (name + PIN), or on the server
`sudo getaload user add NAME`. Everyone sees only their own downloads, files and watch progress.

Forgot a PIN? The owner resets it in the app (Settings, Users), or on the server:
`sudo getaload user pin NAME`. Five wrong PINs lock a name for a minute, then longer.

Docker: `docker compose up -d`, then `docker compose exec getaload getaload user add NAME` (see the guide).

## For contributors and agents

Start with [AGENTS.md](AGENTS.md) (rules, commands, layout) and [docs/STATUS.md](docs/STATUS.md)
(where things stand). The spec is [docs/scope.md](docs/scope.md).
