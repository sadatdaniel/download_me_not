# download_me_not

A self-hosted download server: send it a web link, a video page, a magnet link or a .torrent, it
downloads on your server, and you browse, stream or copy the results to Google Drive from the web
panel, the CLI or (later) the Android app. One static Go binary, or a Docker image with every
helper tool included.

- **Web downloads**, IDM-style: up to 16 connections with work stealing, resume, retry (faster than aria2c with slow routes; see [docs/benchmark.md](docs/benchmark.md))
- **Video pages** through yt-dlp, with format and quality choice
- **Torrents**: stream while downloading, seeding rules, peers and piece map, blocklists, SOCKS5, kill switch, swarm memory
- **File manager** over the API: browse, play with seeking, ZIP folders, copy or move to Google Drive
- **Safe defaults**: token scopes, HTTPS (self-signed with pinning, or automatic certificates), SSRF protection, encrypted secrets, signed self-updates with automatic rollback

## Quick start

See **[docs/deploy.md](docs/deploy.md)** for the full guide.

```sh
# Linux server (systemd): installs, starts, prints your token and a pairing QR code
curl -fsSL https://github.com/sadatdaniel/download_me_not/releases/latest/download/install.sh | sudo sh

# Docker
docker compose up -d && docker compose logs dmn    # the log shows your API token once
```

Then open `https://<server>:8080/` and sign in with the token.

## For contributors and agents

Start with [AGENTS.md](AGENTS.md) (rules, commands, layout) and [docs/STATUS.md](docs/STATUS.md)
(where things stand). The spec is [docs/scope.md](docs/scope.md).
