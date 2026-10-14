<p align="center"><img src="web/icons/Icon-192.png" width="110" alt="GetALoad"></p>
<h1 align="center">GetALoad for Android</h1>
<p align="center"><b>Send anything to your download server, and watch it from anywhere.</b></p>

<p align="center">
  <a href="https://github.com/sadatdaniel/getaload_app/releases/latest"><img src="https://img.shields.io/github/v/release/sadatdaniel/getaload_app?label=download" alt="Download"></a>
</p>

<p align="center">
  <a href="#features">Features</a> · <a href="#install">Install</a> · <a href="#updates">Updates</a> ·
  <a href="#verify-the-download">Verify</a> · <a href="https://github.com/sadatdaniel/getaload">The server</a>
</p>

GetALoad for Android is the remote for your own [GetALoad](https://github.com/sadatdaniel/getaload) download server.
Share a link from any app, paste a magnet link or open a .torrent, and your server downloads it while your phone stays
free. When it is done, or even while it is still downloading, play it right in the app.

> **New to GetALoad?** You need a GetALoad server first. It runs on a free cloud server or a computer at home, and
> takes a few minutes to set up: [get started](https://github.com/sadatdaniel/getaload#get-started).

## Features

**Add downloads**
- Paste or share links, magnet links and .torrent files, several at once.
- For video pages, choose video or audio only, the quality, MP4 or MKV, and subtitles.
- Follow progress live; pause, resume, or start watching before the download finishes.

**Watch and listen**
- A full player: two subtitles at once, audio and subtitle tracks, playback speed, landscape for wide videos, swipe
  down to close.
- Continue watching where you stopped, on any of your devices.
- Every video and song on your server in one Media list.

**Your files**
- Browse your server like a file manager. Open archives like folders, read text and PDFs, view images.
- Rename, move, copy, delete, save to your phone, or copy to your own Google Drive.

**One server for the whole family**
- Each family member signs in on their own phone with their own name and PIN, and gets their own folder, site
  sign-ins and Google Drive. Nobody sees anyone else's downloads.
- Sign in to sites such as YouTube inside the app, so your downloads from them work.

**Stremio add-ons (optional)**
- A Discover tab for browsing titles, with three official Stremio add-ons included: Cinemeta (titles), OpenSubtitles
  (subtitles) and WatchHub (links to legal streaming services). Add-ons that offer streams or downloads are never
  included or suggested; adding them is your choice and your responsibility.
- Add-ons that refuse cloud servers can be looked up from your phone instead. Only the lookup comes from the phone:
  everything you play or download is fetched by your server.

## Install

1. Download the app from the [latest release](https://github.com/sadatdaniel/getaload_app/releases/latest).
   Most phones need **`…-arm64-v8a.apk`**; older 32-bit phones `…-armeabi-v7a.apk`.
2. Open the file and allow the install when Android asks.
3. Enter your server's address, accept its certificate, then pick your name and enter your PIN. That's it: the app
   stays signed in.

## Updates

The app keeps itself up to date. When a new version is out, **Settings → About this app** offers it; one tap
downloads it, checks it, and installs it over the old one. You stay signed in.

## Verify the download

Every release is signed with the same GetALoad key. Its SHA-256 fingerprint is:

```
D5:BD:AE:54:7D:FB:4A:3D:5E:D3:42:F8:03:46:14:0D:45:57:2D:49:90:04:F3:39:A9:C3:48:F9:28:72:1A:D0
```

Android refuses an update signed with any other key, and each release lists the checksums of its files in
`checksums.txt`.

## Privacy

The app talks to your own server, to GitHub to look for app updates, and to add-ons only if you choose to ask them
from your phone. It collects nothing about you. See the
[privacy policy](https://sadatdaniel.github.io/getaload/privacy.html).

## Help and contributing

Questions and bug reports: [issues](https://github.com/sadatdaniel/getaload_app/issues). Building the app yourself:
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).

## License

Not chosen yet. Until a license is added, all rights are reserved.
