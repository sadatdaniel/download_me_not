<p align="center"><img src="https://raw.githubusercontent.com/sadatdaniel/getaload_app/main/web/icons/Icon-192.png" width="110" alt="GetALoad"></p>
<h1 align="center">GetALoad</h1>
<p align="center"><b>Your own download server. Set it up once, no subscription.</b></p>

<p align="center">
  <a href="https://github.com/sadatdaniel/getaload/releases/latest"><img src="https://img.shields.io/github/v/release/sadatdaniel/getaload" alt="Latest release"></a>
  <a href="https://github.com/sadatdaniel/getaload_app/releases/latest"><img src="https://img.shields.io/github/v/release/sadatdaniel/getaload_app?label=android%20app" alt="Android app"></a>
</p>

<p align="center">
  <a href="#why-getaload">Why</a> · <a href="#features">Features</a> · <a href="#get-started">Get started</a> ·
  <a href="docs/deploy.md">Full guide</a> · <a href="#4-connect">Clients</a>
</p>

Cloud downloaders and seedboxes fetch files for you on their servers, and charge you every month for it. GetALoad does
the same job on a server of your own. Send it a link, a video page or a torrent from your browser or your phone; it downloads, keeps
the files, and plays them back to you anywhere.

## Why GetALoad

- **No monthly fee.** Run it on a server you already have, or get one for free: Oracle Cloud's Always Free plan
  includes a small ARM server that is enough for GetALoad ([how to get one](#1-get-a-server)).
- **Fast.** Downloads run on your server, and you can start watching while they are still downloading.
- **One server for the whole family.** Add family members in the app. Everyone gets their own name and PIN, their
  own folder, their own site sign-ins and Google Drive, and nobody sees anyone else's downloads.
- **Easy to look after.** One command to install, and it keeps itself up to date.

## Features

- ✅ **Web downloads**: many connections at once, picks up where it left off after an interruption
- ✅ **Video sites**: YouTube and over a thousand others (via [yt-dlp](https://github.com/yt-dlp/yt-dlp)); choose
  quality, MP4 or MKV, subtitles, or audio only
- ✅ **Torrents**: magnet links and .torrent files, watch while downloading, seeding rules
- ✅ **Media player**: in the apps, based on [mpv](https://mpv.io) (plays nearly any format): two subtitles at once,
  audio and subtitle tracks, speed, resume, and Continue watching across your devices. In the browser, the browser's
  own player.
- ✅ **File manager**: browse folders, open archives without unpacking, view text, PDFs and images, rename, move,
  delete, download
- ✅ **Upload to Google Drive** in one tap, or automatically after a download (more destinations on request)
- ✅ **Family accounts**: everyone gets their own name, PIN, folder, site sign-ins and Google Drive
- ✅ **Site sign-ins**: sign in to sites like YouTube once, and your downloads from them work
- ✅ **Stremio add-ons** (optional): a Discover tab for browsing titles. Three official add-ons are included: Cinemeta
  (titles), OpenSubtitles (subtitles) and WatchHub (links to legal streaming services). Add-ons that offer streams or
  downloads are never included or suggested; adding them is your choice and your responsibility. Add-ons that refuse
  cloud servers can be looked up from your phone instead, while every download still happens on your server.
- ✅ **Use it anywhere**: the full app in any browser, the Android app, or the command line (Linux desktop app coming)
- ✅ **Keeps itself up to date**: the server, its helper tools and the Android app

## Get started

### 1. Get a server

Any Linux machine works: a VPS, a computer at home or a Raspberry Pi (ARM or x86, 64-bit or 32-bit).

No server yet? Oracle Cloud's Always Free plan includes an ARM server (currently 2 cores and 12 GB of memory per
account): [what is included](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm) ·
[step-by-step guide](https://medium.com/@imvinojanv/setup-always-free-vps-with-4-ocpu-24gb-ram-and-200gb-storage-the-ultimate-oracle-cloud-guide-bed5cbf73d34)
(written for the older, larger allowance: choose 2 OCPUs and 12 GB at the shape step, and Ubuntu as the image).

### 2. Install GetALoad

On the server:

```sh
curl -fsSL https://github.com/sadatdaniel/getaload/releases/latest/download/install.sh | sudo sh
```

It asks for your name and a 4-digit PIN, then shows the address to connect to. `sudo getaload connect` shows it again.

### 3. Open the ports

GetALoad needs port **8080** (the app) and, for faster torrents, **42069** (TCP and UDP).

On Ubuntu (including Oracle Cloud's Ubuntu image):

```sh
sudo iptables -I INPUT -p tcp --dport 8080 -j ACCEPT && sudo iptables -I INPUT -p tcp --dport 42069 -j ACCEPT && sudo iptables -I INPUT -p udp --dport 42069 -j ACCEPT && sudo netfilter-persistent save
```

If your server uses ufw instead:

```sh
sudo ufw allow 8080/tcp && sudo ufw allow 42069
```

**On Oracle Cloud, also open them in the console** (both are needed):
1. Menu ☰ → **Networking** → **Virtual cloud networks** → your network → **Subnets** → your subnet.
2. **Default Security List** → **Security rules** (or *Ingress Rules*) → **Add Ingress Rules**.
3. Add three rules, each with Source CIDR `0.0.0.0/0`:
   - IP Protocol **TCP**, Destination Port Range `8080`
   - IP Protocol **TCP**, Destination Port Range `42069`
   - IP Protocol **UDP**, Destination Port Range `42069`
4. **Add Ingress Rules** to save.

### 4. Connect

Sign in with your name and PIN from any of these:

- **Web client**: open the address from step 2 in any browser, on a computer or a phone. Nothing to install; add
  downloads, browse and play your files.
- **Android app**: everything, including the full player, Continue watching and managing your family members.
  Download it from the [releases](https://github.com/sadatdaniel/getaload_app/releases/latest) and enter the same
  address. It keeps itself up to date.
- **Linux desktop** (AppImage): coming soon.

Add your family members in the app: Settings, People on this server.

The [full guide](docs/deploy.md) covers Docker, your own domain, and more.

## Good to know

- **Updates** come by themselves: the server checks daily, and the owner updates it with one tap in the app.
- **Remove it** any time with `sudo getaload uninstall`. Your downloads are never deleted unless you ask.
- **Your responsibility.** GetALoad downloads what you ask it to. Only download what you have the right to, and check
  your hosting provider's rules, especially for torrents.

## Help and contributing

Questions and bug reports: [issues](https://github.com/sadatdaniel/getaload/issues). How it is built and how to work
on it: [AGENTS.md](AGENTS.md), [architecture](docs/architecture.md) and [the spec](docs/scope.md).

## Privacy and license

GetALoad collects nothing about you; see the [privacy policy](https://sadatdaniel.github.io/getaload/privacy.html).
The license is not chosen yet; until one is added, all rights are reserved.
