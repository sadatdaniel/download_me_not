Vendored ES modules (no build step). Source: unpkg.com
- preact 10.29.8 (MIT): dist/preact.module.js, hooks/dist/hooks.module.js
- htm 3.1.1 (Apache-2.0): dist/htm.module.js
Only change: hooks.module.js imports "./preact.module.js" instead of the bare "preact" (no import map,
so the page can keep a strict Content-Security-Policy). Checksums of the files as committed: SHA256SUMS.
