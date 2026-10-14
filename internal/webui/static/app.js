// GetALoad control panel. A plain client of the /v1 API (same as the Android app).
import { h, render } from './vendor/preact.module.js';
import { useState, useEffect, useRef, useCallback } from './vendor/hooks.module.js';
import htm from './vendor/htm.module.js';

const html = htm.bind(h);

// ---- API ----------------------------------------------------------------------------------------

const tokenKey = 'getaload.token';
let token = '';
try { token = localStorage.getItem(tokenKey) || ''; } catch { /* private mode */ }
let onUnauthorized = () => {};

async function api(method, path, body) {
  const headers = { Authorization: 'Bearer ' + token };
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const r = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  if (r.status === 401) { onUnauthorized(); throw new Error('Please sign in again'); }
  if (!r.ok) {
    let msg = 'HTTP ' + r.status;
    try { const e = await r.json(); msg = e.detail || (e.errors && e.errors.map(x => x.message).join('; ')) || msg; } catch { }
    throw new Error(msg);
  }
  if (r.status === 204 || r.status === 202 && r.headers.get('content-length') === '0') return null;
  const text = await r.text();
  return text ? JSON.parse(text) : null;
}

// Server-sent events over fetch (EventSource cannot send the Authorization header).
async function streamProgress(onData, signal) {
  for (;;) {
    try {
      const r = await fetch('/v1/events', { headers: { Authorization: 'Bearer ' + token }, signal });
      if (!r.ok) throw new Error('events ' + r.status);
      const reader = r.body.getReader();
      const dec = new TextDecoder();
      let buf = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, i); buf = buf.slice(i + 2);
          const data = block.split('\n').filter(l => l.startsWith('data:')).map(l => l.slice(5)).join('');
          if (data) { try { onData(JSON.parse(data)); } catch { } }
        }
      }
    } catch (e) {
      if (signal.aborted) return;
    }
    await new Promise(res => setTimeout(res, 3000)); // reconnect
    if (signal.aborted) return;
  }
}

// ---- helpers ------------------------------------------------------------------------------------

const fmtBytes = n => {
  if (n == null || n < 0) return '?';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + ' ' + u[i];
};
const fmtRate = r => (r > 0 ? fmtBytes(r) + '/s' : '');
const pct = (d, t) => (t > 0 ? Math.min(100, (100 * d) / t) : 0);
const isMedia = name => /\.(mp4|mkv|webm|mov|m4v|mp3|m4a|opus|ogg|flac|wav|aac)$/i.test(name);
const isAudio = name => /\.(mp3|m4a|opus|ogg|flac|wav|aac)$/i.test(name);
const fileToBase64 = file => new Promise((res, rej) => {
  const fr = new FileReader();
  fr.onload = () => res(String(fr.result).split(',')[1]);
  fr.onerror = rej;
  fr.readAsDataURL(file);
});

let toastTimer;
function useToast() {
  const [msg, setMsg] = useState('');
  const show = useCallback(m => { setMsg(String(m)); clearTimeout(toastTimer); toastTimer = setTimeout(() => setMsg(''), 5000); }, []);
  return [msg, show];
}

// ---- views --------------------------------------------------------------------------------------

// Login: name + PIN when the server has users (a token for this browser), otherwise a pasted API token.
function Login({ onDone }) {
  const [users, setUsers] = useState(null);
  const [name, setName] = useState('');
  const [t, setT] = useState('');
  const [err, setErr] = useState('');
  useEffect(() => { fetch('/v1/users').then(r => r.json()).then(setUsers).catch(() => setUsers([])); }, []);
  const submit = async e => {
    e.preventDefault();
    try {
      if (users && users.length) {
        token = '';
        const out = await api('POST', '/v1/login', { name, pin: t.trim(), device: 'browser' });
        token = out.token;
      } else {
        token = t.trim();
        await api('GET', '/v1/jobs');
      }
      try { localStorage.setItem(tokenKey, token); } catch { }
      onDone();
    } catch (x) { setErr(x.message); }
  };
  if (!users) return null;
  return html`<div class="card login">
    <h2>GetALoad</h2>
    <form onSubmit=${submit}>
      ${users.length ? html`
        <select value=${name} onChange=${e => setName(e.target.value)} autofocus>
          <option value="">Who are you?</option>
          ${users.map(u => html`<option value=${u.name}>${u.name}</option>`)}
        </select>
        <input type="password" inputmode="numeric" maxlength="4" placeholder="PIN" value=${t} onInput=${e => setT(e.target.value)} style="margin-top:10px" />`
      : html`
        <p class="muted">Paste an API token. Create one on the server with <code>getaload setup</code>.</p>
        <input type="password" placeholder="dmn_..." value=${t} onInput=${e => setT(e.target.value)} autofocus />`}
      <div class="row" style="margin-top:10px"><button class="primary">Sign in</button></div>
      ${err && html`<p class="error">${err}</p>`}
    </form>
  </div>`;
}

function Player({ src, name, onClose }) {
  return html`<div class="modal" onClick=${e => e.target === e.currentTarget && onClose()}>
    <div class="card">
      <div class="row"><div class="grow name">${name}</div><button onClick=${onClose}>Close</button></div>
      ${isAudio(name) ? html`<audio src=${src} controls autoplay />` : html`<video src=${src} controls autoplay playsinline />`}
      <p class="muted">Seeking works while it downloads; the server fetches what you skip to first.</p>
    </div>
  </div>`;
}

function PieceBar({ runs, total }) {
  if (!runs || !total) return null;
  return html`<div class="pieces">${runs.map(r => html`<span style=${{
    width: (100 * r.length) / total + '%',
    background: r.complete ? 'var(--ok)' : r.partial ? 'var(--warn)' : 'var(--bar)',
  }} />`)}</div>`;
}

function TorrentDetails({ job, toast }) {
  const [view, setView] = useState(null);
  const [peers, setPeers] = useState([]);
  useEffect(() => {
    let live = true;
    const load = async () => {
      try {
        const [v, p] = await Promise.all([api('GET', `/v1/jobs/${job.id}/pieces`), api('GET', `/v1/jobs/${job.id}/peers`)]);
        if (live) { setView(v); setPeers(p || []); }
      } catch (e) { if (live) setView({ error: e.message }); }
    };
    load();
    const t = setInterval(load, 3000);
    return () => { live = false; clearInterval(t); };
  }, [job.id]);
  if (!view) return html`<p class="muted">Loading...</p>`;
  if (view.error) return html`<p class="muted">${view.error}</p>`;
  return html`<div>
    <${PieceBar} runs=${view.runs} total=${view.pieces} />
    <div class="muted">pieces ${view.complete}/${view.pieces} · peers ${view.peers} · seeders ${view.seeders} · availability ${(view.availability || 0).toFixed(2)}</div>
    ${view.hint && html`<p class="hint">${view.hint}</p>`}
    ${peers.length > 0 && html`<table><tr><th>Peer</th><th class="hide-sm">Client</th><th>Has</th><th>Down</th></tr>
      ${peers.map(p => html`<tr><td>${p.addr}</td><td class="hide-sm">${p.client}</td><td>${Math.round(p.progress * 100)}%</td><td>${fmtRate(p.downRate)}</td></tr>`)}
    </table>`}
  </div>`;
}

function JobRow({ job, prog, onPlay, reload, toast }) {
  const [open, setOpen] = useState(false);
  const done = prog ? prog.done : job.done;
  const total = prog && prog.total > 0 ? prog.total : job.total;
  const act = async (fn, ok) => { try { await fn(); ok && toast(ok); reload(); } catch (e) { toast(e.message); } };
  const running = !['done', 'failed', 'paused'].includes(job.state);
  const play = async () => {
    try { const r = await api('POST', `/v1/jobs/${job.id}/sign`); onPlay(r.url, job.name || job.source); } catch (e) { toast(e.message); }
  };
  const cancel = () => {
    const del = confirm('Stop and forget this download?\n\nOK keeps the files. To also delete its files, use "Delete files".');
    del && act(() => api('DELETE', `/v1/jobs/${job.id}`), 'Removed');
  };
  const cancelDelete = () => confirm('Stop and DELETE its files?') && act(() => api('DELETE', `/v1/jobs/${job.id}?deleteFiles=true`), 'Removed with files');
  return html`<div class="card">
    <div class="row">
      <div class="grow">
        <div class="name">${job.name || job.source}</div>
        <div class="muted">${job.kind}${job.mode === 'stream' ? ' · stream' : ''} · ${fmtBytes(done)}${total > 0 ? ' of ' + fmtBytes(total) : ''}${prog && prog.rate ? ' · ' + fmtRate(prog.rate) : ''}${prog && prog.peers ? ' · ' + prog.peers + ' peers' : ''}</div>
      </div>
      <span class=${'badge ' + job.state}>${job.state}</span>
    </div>
    ${(running || job.state === 'paused') && html`<div class="bar"><div style=${{ width: pct(done, total) + '%' }} /></div>`}
    ${prog && prog.hint && prog.hint !== 'Complete.' && html`<p class="hint">${prog.hint}</p>`}
    ${job.error && html`<p class="error">${job.error}</p>`}
    <div class="row" style="margin-top:10px">
      ${job.want === 'paused' || job.state === 'failed'
        ? html`<button onClick=${() => act(() => api('POST', `/v1/jobs/${job.id}/resume`))}>${job.state === 'failed' ? 'Retry' : 'Resume'}</button>`
        : job.state !== 'done' && html`<button onClick=${() => act(() => api('POST', `/v1/jobs/${job.id}/pause`))}>Pause</button>`}
      ${(job.state === 'done' || job.mode === 'stream' || job.kind === 'torrent') && job.kind !== 'copy' && html`<button onClick=${play}>Play</button>`}
      ${running && job.kind !== 'copy' && html`<button onClick=${() => act(() => api('PUT', `/v1/jobs/${job.id}/mode`, { mode: job.mode === 'stream' ? 'standard' : 'stream' }))}>${job.mode === 'stream' ? 'Standard mode' : 'Stream mode'}</button>`}
      ${job.state === 'done' && job.path && job.kind !== 'copy' && html`<button onClick=${() => act(() => api('POST', '/v1/copy', { path: job.path }), 'Copy to Drive queued')}>Copy to Drive</button>`}
      ${job.kind === 'torrent' && running && html`<button onClick=${() => setOpen(!open)}>${open ? 'Hide details' : 'Peers & pieces'}</button>`}
      <span class="grow" />
      <button onClick=${cancel}>Remove</button>
      <button class="danger" onClick=${cancelDelete}>Delete files</button>
    </div>
    ${open && html`<${TorrentDetails} job=${job} toast=${toast} />`}
  </div>`;
}

function Downloads({ toast, onPlay }) {
  const [jobs, setJobs] = useState(null);
  const [prog, setProg] = useState({});
  const load = useCallback(async () => { try { setJobs(await api('GET', '/v1/jobs')); } catch (e) { toast(e.message); } }, []);
  useEffect(() => {
    load();
    const t = setInterval(load, 4000);
    const ac = new AbortController();
    streamProgress(list => setProg(Object.fromEntries((list || []).map(p => [p.id, p]))), ac.signal);
    return () => { clearInterval(t); ac.abort(); };
  }, []);
  if (!jobs) return html`<p class="muted">Loading...</p>`;
  if (!jobs.length) return html`<div class="card"><p>No downloads yet.</p><a href="#/add" class="btn">Add one</a></div>`;
  return jobs.map(j => html`<${JobRow} key=${j.id} job=${j} prog=${prog[j.id]} onPlay=${onPlay} reload=${load} toast=${toast} />`);
}

function Add({ toast, go }) {
  const [src, setSrc] = useState('');
  const [file, setFile] = useState(null);
  const [probe, setProbe] = useState(null);
  const [format, setFormat] = useState('best');
  const [mode, setMode] = useState('standard');
  const [extract, setExtract] = useState(false);
  const [drive, setDrive] = useState(false);
  const [busy, setBusy] = useState(false);
  const check = async () => {
    setBusy(true); setProbe(null);
    try { setProbe(await api('POST', '/v1/probe', { url: src.trim() })); } catch (e) { toast(e.message); }
    setBusy(false);
  };
  const add = async e => {
    e.preventDefault();
    setBusy(true);
    try {
      const body = { mode };
      if (file) body.torrent = await fileToBase64(file); else body.source = src.trim();
      if (probe && probe.kind === 'video') body.format = format;
      if (extract || drive) body.post = { extract, drive };
      await api('POST', '/v1/jobs', body);
      toast('Added'); go('#/');
    } catch (x) { toast(x.message); }
    setBusy(false);
  };
  return html`<form class="card" onSubmit=${add}>
    <h3>Add a download</h3>
    <label>Link: web file, video page or magnet</label>
    <textarea value=${src} onInput=${e => { setSrc(e.target.value); setProbe(null); }} placeholder="https://... or magnet:?xt=..." disabled=${!!file} />
    <label>or a .torrent file</label>
    <input type="file" accept=".torrent,application/x-bittorrent" onChange=${e => setFile(e.target.files[0] || null)} />
    ${!file && src.trim().startsWith('http') && html`<div class="row" style="margin-top:8px"><button type="button" onClick=${check} disabled=${busy}>Check link</button>
      ${probe && html`<span class="muted">${probe.kind === 'video' ? 'Video page: ' + probe.video.title : 'File: ' + (probe.name || '') + (probe.size ? ' (' + fmtBytes(probe.size) + ')' : '')}</span>`}</div>`}
    ${probe && probe.kind === 'video' && html`<div>
      <label>Quality</label>
      <select value=${format} onChange=${e => setFormat(e.target.value)}>
        ${probe.presets.map(p => html`<option value=${p.name}>${p.name}</option>`)}
        <optgroup label="Exact formats">${probe.video.formats.map(f => html`<option value=${f.id}>${f.id} · ${f.ext} · ${f.resolution || 'audio'}${f.size ? ' · ' + fmtBytes(f.size) : ''}</option>`)}</optgroup>
      </select>
    </div>`}
    <label>Mode</label>
    <div>
      <label class="inline"><input type="radio" checked=${mode === 'standard'} onChange=${() => setMode('standard')} /> Standard (fastest)</label>
      <label class="inline"><input type="radio" checked=${mode === 'stream'} onChange=${() => setMode('stream')} /> Stream (watch while downloading)</label>
    </div>
    <label>When done</label>
    <div>
      <label class="inline"><input type="checkbox" checked=${extract} onChange=${e => setExtract(e.target.checked)} /> Unpack archives</label>
      <label class="inline"><input type="checkbox" checked=${drive} onChange=${e => setDrive(e.target.checked)} /> Copy to Google Drive</label>
    </div>
    <div class="row" style="margin-top:14px"><button class="primary" disabled=${busy || (!file && !src.trim())}>Add</button></div>
  </form>`;
}

function Files({ toast, onPlay }) {
  const [path, setPath] = useState('/');
  const [entries, setEntries] = useState(null);
  const [usage, setUsage] = useState(null);
  const load = useCallback(async p => {
    try {
      setEntries(await api('GET', '/v1/fs?path=' + encodeURIComponent(p)));
      setUsage(await api('GET', '/v1/fs/usage'));
    } catch (e) { toast(e.message); setEntries([]); }
  }, []);
  useEffect(() => { load(path); }, [path]);
  const join = name => (path === '/' ? '' : path) + '/' + name;
  const signed = async p => (await api('POST', '/v1/fs/sign', { path: p })).url;
  const act = async (fn, ok) => { try { await fn(); ok && toast(ok); load(path); } catch (e) { toast(e.message); } };
  const parts = path.split('/').filter(Boolean);
  return html`<div class="card">
    <div class="row">
      <div class="grow crumbs">
        <a onClick=${() => setPath('/')}>Downloads</a>
        ${parts.map((p, i) => html` / <a onClick=${() => setPath('/' + parts.slice(0, i + 1).join('/'))}>${p}</a>`)}
      </div>
      <button onClick=${() => { const n = prompt('New folder name'); n && act(() => api('POST', '/v1/fs/mkdir', { path: join(n) })); }}>New folder</button>
      ${path !== '/' && html`<button onClick=${async () => { try { location.href = (await signed(path)).replace('/v1/raw/', '/v1/zip/'); } catch (e) { toast(e.message); } }}>Download as ZIP</button>`}
    </div>
    ${usage && html`<div class="muted" style="margin:8px 0">${fmtBytes(usage.free)} free of ${fmtBytes(usage.total)}</div>`}
    ${!entries ? html`<p class="muted">Loading...</p>` : !entries.length ? html`<p class="muted">Empty.</p>` : html`<table>
      <tr><th>Name</th><th class="hide-sm">Size</th><th class="hide-sm">Modified</th><th /></tr>
      ${entries.sort((a, b) => (b.dir - a.dir) || a.name.localeCompare(b.name)).map(e => html`<tr>
        <td class="entry name" onClick=${() => e.dir && setPath(e.path)}>${e.dir ? '📁 ' : ''}${e.name}</td>
        <td class="hide-sm">${e.dir ? '' : fmtBytes(e.size)}</td>
        <td class="hide-sm muted">${new Date(e.modTime).toLocaleString()}</td>
        <td class="actions">
          ${!e.dir && isMedia(e.name) && html`<button onClick=${async () => { try { onPlay(await signed(e.path), e.name); } catch (x) { toast(x.message); } }}>Play</button>`}
          ${!e.dir && html` <button onClick=${async () => { try { location.href = await signed(e.path); } catch (x) { toast(x.message); } }}>Download</button>`}
          ${' '}<button onClick=${() => { const n = prompt('Rename to', e.name); n && n !== e.name && act(() => api('POST', '/v1/fs/move', { from: e.path, to: join(n) })); }}>Rename</button>
          ${' '}<button onClick=${() => act(() => api('POST', '/v1/copy', { path: e.path }), 'Copy to Drive queued')}>To Drive</button>
          ${' '}<button class="danger" onClick=${() => confirm('Delete ' + e.name + '?') && act(() => api('DELETE', '/v1/fs?path=' + encodeURIComponent(e.path)), 'Deleted')}>Delete</button>
        </td></tr>`)}
    </table>`}
  </div>`;
}

// SchemaForm renders a provider's settings from its JSON Schema: new providers need no UI change.
function SchemaForm({ name, schema, toast }) {
  const [values, setValues] = useState(null);
  const [edits, setEdits] = useState({});
  useEffect(() => { api('GET', `/v1/providers/${name}/settings`).then(r => setValues(r.values)).catch(e => setValues({ _error: e.message })); }, [name]);
  if (!values) return html`<p class="muted">Loading...</p>`;
  if (values._error) return html`<p class="muted">${values._error}</p>`;
  const props = (schema && schema.properties) || {};
  const keys = Object.keys(props).filter(k => !props[k].readOnly);
  if (!keys.length) return html`<p class="muted">No settings.</p>`;
  const save = async e => {
    e.preventDefault();
    try {
      const r = await api('PUT', `/v1/providers/${name}/settings`, edits);
      setValues(r.values); setEdits({});
      toast(r.restart && r.restart.length ? 'Saved; restart the server for: ' + r.restart.join(', ') : 'Saved');
    } catch (x) { toast(x.message); }
  };
  const field = k => {
    const p = props[k];
    const v = k in edits ? edits[k] : values[k];
    const set = val => setEdits({ ...edits, [k]: val });
    const note = p['x-restart'] ? ' (after restart)' : '';
    if (p.type === 'boolean') return html`<label class="inline" style="margin:6px 12px 6px 0"><input type="checkbox" checked=${!!v} onChange=${e => set(e.target.checked)} /> ${p.title || k}${note}</label>`;
    if (p.enum) return html`<label>${p.title || k}${note}</label><select value=${v} onChange=${e => set(e.target.value)}>${p.enum.map(o => html`<option>${o}</option>`)}</select>`;
    if (p.writeOnly) return html`<label>${p.title || k}${note} ${v === true ? '(set)' : ''}</label><input type="password" placeholder=${v === true ? 'leave empty to keep' : ''} value=${typeof v === 'string' ? v : ''} onInput=${e => set(e.target.value)} />`;
    if (p.type === 'integer' || p.type === 'number') return html`<label>${p.title || k}${note}</label><input type="number" min=${p.minimum} max=${p.maximum} value=${v} onInput=${e => set(Number(e.target.value))} />`;
    return html`<label>${p.title || k}${note}</label><input value=${v ?? ''} onInput=${e => set(e.target.value)} />`;
  };
  return html`<form onSubmit=${save}>${keys.map(field)}<div class="row" style="margin-top:10px"><button class="primary" disabled=${!Object.keys(edits).length}>Save</button></div></form>`;
}

function DriveConnect({ toast }) {
  const [si, setSi] = useState(null);
  const [state, setState] = useState('');
  const start = async () => {
    try {
      const r = await api('POST', '/v1/providers/gdrive/signin');
      setSi(r); setState('pending');
      const poll = setInterval(async () => {
        try {
          const s = await api('GET', '/v1/providers/gdrive/signin');
          if (s.state !== 'pending') { clearInterval(poll); setState(s.state); toast(s.state === 'done' ? 'Google Drive connected' : 'Sign-in failed: ' + s.error); }
        } catch { clearInterval(poll); }
      }, 3000);
    } catch (e) { toast(e.message); }
  };
  return html`<div>
    <button class="primary" onClick=${start}>Connect Google Drive</button>
    ${si && state === 'pending' && html`<div style="margin-top:10px">
      <p>Open <a href=${si.verificationUrl} target="_blank" rel="noopener">${si.verificationUrl}</a> on any device and enter:</p>
      <div class="code">${si.userCode}</div><p class="muted">Waiting for approval...</p></div>`}
    ${state === 'done' && html`<p>Connected.</p>`}
  </div>`;
}

function Settings({ toast }) {
  const [providers, setProviders] = useState([]);
  const [open, setOpen] = useState('');
  const [limits, setLimits] = useState(null);
  const [cookies, setCookies] = useState([]);
  const [tools, setTools] = useState([]);
  const [upd, setUpd] = useState(null);
  const [ck, setCk] = useState({ domain: '', content: '', userAgent: '' });
  const load = async () => {
    try {
      const [p, l, c, t] = await Promise.all([api('GET', '/v1/providers'), api('GET', '/v1/limits'), api('GET', '/v1/cookies'), api('GET', '/v1/doctor')]);
      setProviders(p); setLimits(l); setCookies(c); setTools(t);
      api('GET', '/v1/update').then(setUpd).catch(() => {});
    } catch (e) { toast(e.message); }
  };
  useEffect(() => { load(); }, []);
  const act = async (fn, ok) => { try { await fn(); ok && toast(ok); load(); } catch (e) { toast(e.message); } };
  const mib = n => (n ? (n / 1048576).toString() : '');
  return html`
    <div class="card"><h3>Speed limits</h3>
      ${limits && html`<form class="row" onSubmit=${e => { e.preventDefault(); const f = new FormData(e.target);
        act(() => api('PUT', '/v1/limits', { down: Math.round(Number(f.get('down') || 0) * 1048576), up: Math.round(Number(f.get('up') || 0) * 1048576) }), 'Limits applied'); }}>
        <div class="grow"><label>Download MiB/s (empty = unlimited)</label><input name="down" type="number" min="0" step="0.1" value=${mib(limits.down)} /></div>
        <div class="grow"><label>Upload MiB/s</label><input name="up" type="number" min="0" step="0.1" value=${mib(limits.up)} /></div>
        <button class="primary" style="align-self:flex-end">Apply</button></form>`}
    </div>
    <div class="card"><h3>Providers</h3>
      ${providers.map(p => html`<div style="border-top:1px solid var(--line);padding:10px 0">
        <div class="row">
          <div class="grow"><div class="name">${p.name}</div><div class="muted">${p.description}</div>${p.reason && html`<div class="hint">${p.reason}</div>`}</div>
          <span class=${'badge ' + (p.status === 'ready' ? 'done' : '')}>${p.status}</span>
          ${!p.alwaysOn && html`<button onClick=${() => act(() => api('PUT', `/v1/providers/${p.name}/enabled`, { enabled: !p.enabled }))}>${p.enabled ? 'Turn off' : 'Turn on'}</button>`}
          <button onClick=${() => setOpen(open === p.name ? '' : p.name)}>${open === p.name ? 'Close' : 'Settings'}</button>
        </div>
        ${open === p.name && html`<div style="margin-top:8px">
          <${SchemaForm} name=${p.name} schema=${p.schema} toast=${toast} />
          ${p.name === 'gdrive' && html`<div style="margin-top:12px"><${DriveConnect} toast=${toast} /></div>`}
        </div>`}
      </div>`)}
    </div>
    <div class="card"><h3>Cookies for sites that need a login</h3>
      <p class="muted">Export cookies.txt from your browser for one site. They are stored encrypted, sent only over HTTPS, and never shown again.</p>
      ${cookies.length > 0 && html`<table>${cookies.map(c => html`<tr><td>${c.domain}</td><td class="muted">${c.count} cookies${c.expired ? ', ' + c.expired + ' expired' : ''}</td>
        <td class="actions"><button class="danger" onClick=${() => act(() => api('DELETE', '/v1/cookies/' + encodeURIComponent(c.domain)), 'Removed')}>Remove</button></td></tr>`)}</table>`}
      <form onSubmit=${e => { e.preventDefault(); act(() => api('POST', '/v1/cookies', ck), 'Imported'); setCk({ domain: '', content: '', userAgent: '' }); }}>
        <label>Domain</label><input value=${ck.domain} placeholder="example.com" onInput=${e => setCk({ ...ck, domain: e.target.value })} />
        <label>cookies.txt</label><textarea value=${ck.content} onInput=${e => setCk({ ...ck, content: e.target.value })} />
        <label>Browser User-Agent (optional)</label><input value=${ck.userAgent} onInput=${e => setCk({ ...ck, userAgent: e.target.value })} />
        <div class="row" style="margin-top:10px"><button class="primary" disabled=${!ck.domain || !ck.content}>Import</button></div>
      </form>
    </div>
    <div class="card"><h3>Helper tools</h3>
      <table>${tools.map(t => html`<tr><td class="name">${t.name}</td><td class="muted">${t.found ? (t.version || 'installed') : 'missing'} · ${t.features}</td>
        <td class="actions">${t.managed && html`<button onClick=${() => act(() => api('POST', `/v1/tools/${t.name}/install`), t.name + ' up to date')}>${t.found ? 'Update' : 'Install'}</button>`}</td></tr>`)}</table>
    </div>
    <div class="card"><h3>Server</h3>
      ${upd ? html`<p>Version ${upd.current}${upd.available ? html` · <b>update ${upd.latest} available</b> <button onClick=${() => confirm('Install ' + upd.latest + ' and restart?') && act(() => api('POST', '/v1/update/apply'), 'Updating; the server restarts')}>Update now</button>` : ' · up to date'}</p>` : html`<p class="muted">Update status unavailable.</p>`}
    </div>`;
}

function App() {
  const [authed, setAuthed] = useState(!!token);
  const [route, setRoute] = useState(location.hash || '#/');
  const [player, setPlayer] = useState(null);
  const [msg, toast] = useToast();
  onUnauthorized = () => { token = ''; try { localStorage.removeItem(tokenKey); } catch { } setAuthed(false); };
  useEffect(() => {
    const f = () => { setRoute(location.hash || '#/'); setPlayer(null); };
    const esc = e => e.key === 'Escape' && setPlayer(null);
    addEventListener('hashchange', f); addEventListener('keydown', esc);
    return () => { removeEventListener('hashchange', f); removeEventListener('keydown', esc); };
  }, []);
  const go = r => { location.hash = r; };
  if (!authed) return html`<${Login} onDone=${() => setAuthed(true)} />`;
  const nav = [['#/', 'Downloads'], ['#/add', 'Add'], ['#/files', 'Files'], ['#/settings', 'Settings']];
  const onPlay = (src, name) => setPlayer({ src, name });
  return html`
    <header>
      <span class="brand">GetALoad</span>
      <nav>${nav.map(([r, t]) => html`<a href=${r} class=${route === r ? 'on' : ''}>${t}</a>`)}</nav>
      <button onClick=${onUnauthorized}>Sign out</button>
    </header>
    <main>
      ${route === '#/add' ? html`<${Add} toast=${toast} go=${go} />`
        : route === '#/files' ? html`<${Files} toast=${toast} onPlay=${onPlay} />`
        : route === '#/settings' ? html`<${Settings} toast=${toast} />`
        : html`<${Downloads} toast=${toast} onPlay=${onPlay} />`}
    </main>
    ${player && html`<${Player} ...${player} onClose=${() => setPlayer(null)} />`}
    ${msg && html`<div class="toast">${msg}</div>`}`;
}

render(html`<${App} />`, document.getElementById('app'));
