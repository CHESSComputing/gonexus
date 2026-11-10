/* NeXus Browser – dependency-free front end for the gonexus HTTP server.
 *
 * Endpoints used:  /files  /group  /field  /tree
 * Optional query parameter  ?api=http://host:8081  points the UI at another
 * origin (that server must then allow CORS). Deep links use the URL hash:
 *   #key=chess/results.nxs&path=saxs_azimuthal/data/I
 */
(() => {
'use strict';

const API = (new URLSearchParams(location.search).get('api') || '').replace(/\/+$/, '');
const MAX_PLOT_POINTS = 50000;      // default 1-D window
const HEATMAP_ELEMS   = 200000;     // default heatmap block size
const HEATMAP_MAX     = 1000000;    // UI cap for a heatmap block

/* ------------------------------------------------------------ helpers */
const $ = (s, r = document) => r.querySelector(s);
function h(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  if (attrs) for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (k === 'text') e.textContent = v;
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const c of kids.flat()) if (c != null && c !== false) e.append(c.nodeType ? c : document.createTextNode(String(c)));
  return e;
}
const joinPath = (p, n) => p ? p + '/' + n : n;
const prod = a => (a || []).reduce((x, y) => x * y, 1);
const shapeStr = s => (s && s.length) ? s.join('×') : 'scalar';
const isNum = v => typeof v === 'number';

function fmt(v) {
  if (typeof v !== 'number') return String(v);
  if (Number.isInteger(v)) return String(v);
  if (!isFinite(v)) return String(v);
  const a = Math.abs(v);
  if (a !== 0 && (a < 1e-4 || a >= 1e7)) return v.toExponential(6).replace(/\.?0+e/, 'e');
  return String(Number(v.toPrecision(10)));
}
function fmtBytes(n) {
  if (n == null) return '';
  const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + ' ' + u[i];
}
function niceTicks(lo, hi, n = 6) {
  if (!(hi > lo)) return [lo];
  const span = hi - lo, raw = span / n, p = Math.pow(10, Math.floor(Math.log10(raw)));
  const f = raw / p, step = (f < 1.5 ? 1 : f < 3 ? 2 : f < 7 ? 5 : 10) * p;
  const out = [];
  for (let t = Math.ceil(lo / step) * step; t <= hi + step * 1e-9; t += step) out.push(Math.abs(t) < step * 1e-9 ? 0 : t);
  return out;
}
function cssVar(n) { return getComputedStyle(document.documentElement).getPropertyValue(n).trim(); }

let statusTimer;
function status(msg, isErr, sticky) {
  const el = $('#status');
  el.textContent = msg; el.className = 'status show' + (isErr ? ' err' : '');
  clearTimeout(statusTimer);
  if (!sticky) statusTimer = setTimeout(() => el.classList.remove('show'), isErr ? 6000 : 1800);
}
function hideStatus() { $('#status').classList.remove('show'); }

async function api(path, params) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) if (v !== undefined && v !== null && v !== '') q.set(k, v);
  const url = `${API}${path}?${q}`;
  const res = await fetch(url);
  const text = await res.text();
  if (!res.ok) {
    let msg = text; try { msg = JSON.parse(text).error || text; } catch (_) {}
    throw new Error(`${res.status}: ${msg}`);
  }
  return path === '/tree' ? text : JSON.parse(text);
}
const apiUrl = (path, params) => new URL(`${API}${path}?${new URLSearchParams(params)}`, location.href).href;

function download(name, text, mime) {
  const a = h('a', { href: URL.createObjectURL(new Blob([text], { type: mime })), download: name });
  document.body.append(a); a.click(); a.remove();
}

/* ------------------------------------------------------------ state */
const S = {
  key: '', root: null, selected: null, tab: 'detail', rawCache: {},
  nodes: new Map(),           // path -> node model
  fieldToken: 0,
};

/* ------------------------------------------------------------ tree */
function makeNode(parent, brief, path) {
  const n = { path, name: brief.name, type: brief.type, cls: brief.class || '', dtype: brief.dtype, shape: brief.shape,
              units: brief.units, parent, children: [], loaded: false, expanded: false, attrs: null };
  S.nodes.set(path, n);
  return n;
}
const isGroup = n => n.type === 'group';

function renderNode(n, container) {
  const twisty = h('span', { class: 'twisty' }, isGroup(n) ? '▸' : '');
  const ico = h('span', { class: 'ico' }, isGroup(n) ? '▣' : (n.shape && n.shape.length ? '≣' : '•'));
  const label = n.parent === null ? `root` : n.name;
  const meta = isGroup(n) ? '' : `${n.dtype || ''}${n.shape && n.shape.length ? '(' + n.shape.join('×') + ')' : ''}`;
  const row = h('div', { class: 'node ' + (isGroup(n) ? 'group' : 'field'), role: 'treeitem', 'data-path': n.path,
                         title: n.path || '/' },
    twisty, ico, h('span', { class: 'nm' }, label),
    isGroup(n) && n.cls ? h('span', { class: 'cls' }, n.cls) : null,
    meta ? h('span', { class: 'meta' }, meta) : null);
  const kids = h('div', { class: 'children hidden', role: 'group' });
  const item = h('div', { class: 'item' }, row, kids);
  n.el = item; n.row = row; n.kidsEl = kids; n.twisty = twisty;
  row.addEventListener('click', e => {
    if (isGroup(n) && e.target === twisty) { toggle(n); return; }
    select(n);
    if (isGroup(n)) toggle(n, true);
  });
  container.append(item);
}

function loadChildren(n) {
  if (n.loaded) return Promise.resolve();
  if (n.loading) return n.loading;           // share one in-flight request (select + toggle race)
  n.loading = (async () => {
    const g = await api('/group', { key: S.key, path: n.path });
    n.attrs = g.attrs || {}; n.cls = g.class || n.cls;
    for (const c of (g.children || [])) {
      const cn = makeNode(n, c, joinPath(n.path, c.name));
      n.children.push(cn);
      renderNode(cn, n.kidsEl);
    }
    n.loaded = true;
  })().finally(() => { n.loading = null; });
  return n.loading;
}
async function toggle(n, forceOpen) {
  if (!isGroup(n)) return;
  const open = forceOpen ? true : !n.expanded;
  if (open) {
    try { await loadChildren(n); } catch (e) { status(e.message, true); return; }
  }
  n.expanded = open;
  n.kidsEl.classList.toggle('hidden', !open);
  n.twisty.textContent = open ? '▾' : '▸';
  applyFilter();
}
async function expandAll() {
  if (!S.root) return;
  let queue = [S.root], count = 0;
  while (queue.length && count < 3000) {
    const batch = queue.splice(0, 6);
    status(`Loading groups… ${count}`, false, true);
    await Promise.all(batch.map(async n => {
      try { await toggle(n, true); } catch (_) {}
      count++;
      for (const c of n.children) if (isGroup(c)) queue.push(c);
    }));
  }
  hideStatus();
}
function collapseAll() {
  for (const n of S.nodes.values()) if (isGroup(n) && n !== S.root && n.expanded) {
    n.expanded = false; n.kidsEl.classList.add('hidden'); n.twisty.textContent = '▸';
  }
  applyFilter();
}

function applyFilter() {
  const q = $('#filterInput').value.trim().toLowerCase();
  if (!S.root) return;
  const walk = n => {
    const self = !q || n.name.toLowerCase().includes(q) || (n.cls || '').toLowerCase().includes(q);
    let any = false;
    for (const c of n.children) if (walk(c)) any = true;
    const show = !q || self || any;
    n.el.classList.toggle('hidden', !show && n !== S.root);
    if (q && any) { n.kidsEl.classList.remove('hidden'); n.twisty.textContent = '▾'; }
    else if (isGroup(n)) { n.kidsEl.classList.toggle('hidden', !n.expanded); n.twisty.textContent = n.expanded ? '▾' : '▸'; }
    return show;
  };
  walk(S.root);
}

function select(n, noHash) {
  if (S.selected && S.selected.row) S.selected.row.classList.remove('sel');
  S.selected = n; n.row.classList.add('sel');
  n.row.scrollIntoView({ block: 'nearest' });
  if (!noHash) updateHash();
  setTab('detail');
  isGroup(n) ? showGroup(n) : showField(n);
}

async function revealPath(path) {
  if (!S.root) return;
  const parts = path.split('/').filter(Boolean);
  let cur = S.root;
  for (let i = 0; i < parts.length; i++) {
    await toggle(cur, true);                          // load + open the parent
    const next = cur.children.find(c => c.name === parts[i]);
    if (!next) { status(`Path not found: ${path}`, true); break; }
    cur = next;
  }
  select(cur, true);
  if (isGroup(cur)) toggle(cur, true);
}

function updateHash() {
  const p = new URLSearchParams();
  if (S.key) p.set('key', S.key);
  if (S.selected && S.selected.path) p.set('path', S.selected.path);
  history.replaceState(null, '', '#' + p);
}

/* keyboard navigation */
$('#tree').addEventListener('keydown', e => {
  const rows = [...document.querySelectorAll('#tree .node')].filter(r => r.offsetParent !== null);
  if (!rows.length) return;
  const cur = S.selected ? rows.indexOf(S.selected.row) : -1;
  const go = i => { const r = rows[Math.max(0, Math.min(rows.length - 1, i))]; select(S.nodes.get(r.dataset.path)); };
  if (e.key === 'ArrowDown') { go(cur + 1); e.preventDefault(); }
  else if (e.key === 'ArrowUp') { go(cur - 1); e.preventDefault(); }
  else if (e.key === 'ArrowRight' && S.selected && isGroup(S.selected)) { toggle(S.selected, true); e.preventDefault(); }
  else if (e.key === 'ArrowLeft' && S.selected) {
    if (isGroup(S.selected) && S.selected.expanded) toggle(S.selected, false);
    else if (S.selected.parent) select(S.selected.parent);
    e.preventDefault();
  } else if (e.key === 'Enter' && S.selected && isGroup(S.selected)) toggle(S.selected);
});

/* ------------------------------------------------------------ detail: shared bits */
function crumbs(n) {
  const parts = n.path ? n.path.split('/') : [];
  const out = [h('a', { onclick: () => select(S.root) }, '/')];
  let p = '';
  parts.forEach((part, i) => {
    p = joinPath(p, part);
    const pp = p;
    out.push(i === parts.length - 1 ? h('span', null, part) : h('a', { onclick: () => select(S.nodes.get(pp) || S.root) }, part), i < parts.length - 1 ? ' / ' : '');
  });
  return h('div', { class: 'crumbs' }, ...out);
}
function attrsTable(attrs) {
  const keys = Object.keys(attrs || {});
  if (!keys.length) return h('div', { class: 'hint' }, 'No attributes.');
  return h('table', { class: 'kv' }, ...keys.map(k => h('tr', null, h('td', null, '@' + k),
    h('td', null, typeof attrs[k] === 'object' ? JSON.stringify(attrs[k]) : String(attrs[k])))));
}
function prettyScalar(v) {
  if (typeof v === 'string') {
    const t = v.trim();
    if ((t[0] === '{' || t[0] === '[')) { try { return { text: JSON.stringify(JSON.parse(t), null, 2), json: true }; } catch (_) {} }
    return { text: v };
  }
  if (Array.isArray(v)) return { text: JSON.stringify(v) };
  return { text: String(v) };
}

/* ------------------------------------------------------------ detail: group */
async function showGroup(n) {
  const d = $('#detail'); d.replaceChildren(h('div', { class: 'hint' }, 'Loading…'));
  try { await loadChildren(n); } catch (e) { d.replaceChildren(h('div', { class: 'err' }, e.message)); return; }
  const attrs = n.attrs || {};
  const rows = n.children.map(c => h('tr', { class: 'clickable', onclick: () => { toggle(n, true); select(c); } },
    h('td', null, isGroup(c) ? '▣ ' + c.name : '≣ ' + c.name),
    h('td', null, isGroup(c) ? c.cls : 'field'),
    h('td', null, c.dtype || ''),
    h('td', null, c.shape && c.shape.length ? c.shape.join('×') : (isGroup(c) ? '' : 'scalar')),
    h('td', null, c.units || '')));
  const kids = [
    crumbs(n),
    h('h2', null, n.parent ? n.name : 'root'),
    h('div', { class: 'badges' }, h('span', { class: 'badge' }, n.cls || 'group'), h('span', { class: 'badge' }, `${n.children.length} children`)),
    h('h3', null, 'Attributes'), attrsTable(attrs),
    h('h3', null, 'Children'),
    rows.length ? h('div', { class: 'scroll' }, h('table', { class: 'grid' },
      h('thead', null, h('tr', null, ...['Name', 'Class', 'Dtype', 'Shape', 'Units'].map(t => h('th', null, t)))), h('tbody', null, rows)))
                : h('div', { class: 'hint' }, 'Empty group.'),
  ];
  if (typeof attrs.signal === 'string') {
    const sig = n.children.find(c => c.name === attrs.signal);
    if (sig) kids.splice(3, 0, h('div', { class: 'toolbar' }, h('button', { class: 'btn', onclick: () => select(sig) }, `Plot signal “${attrs.signal}”`)));
  }
  d.replaceChildren(...kids);
}

/* ------------------------------------------------------------ plots */
class LinePlot {
  constructor(height = 360) {
    this.height = height;
    this.canvas = h('canvas'); this.over = h('canvas', { style: 'position:absolute;left:0;top:0' });
    this.readout = h('div', { class: 'readout hidden' });
    this.el = h('div', { class: 'plotwrap' }, this.canvas, this.over, this.readout);
    this.m = { l: 70, r: 14, t: 12, b: 38 };
    this.xs = []; this.ys = []; this.opt = {};
    const ro = new ResizeObserver(() => this.draw()); ro.observe(this.el);
    this.over.addEventListener('mousemove', e => this.hover(e));
    this.over.addEventListener('mouseleave', () => { this.clearOver(); this.readout.classList.add('hidden'); });
    this.over.addEventListener('mousedown', e => this.dragStart(e));
    this.over.addEventListener('dblclick', () => { this.resetView(); this.draw(); });
    this.over.style.cursor = 'crosshair';
  }
  set(xs, ys, opt) { this.xs = xs; this.ys = ys; this.opt = opt || {}; this.resetView(); this.draw(); }
  ty(y) { return this.opt.logY ? (y > 0 ? Math.log10(y) : NaN) : y; }
  resetView() {
    let x0 = Infinity, x1 = -Infinity, y0 = Infinity, y1 = -Infinity;
    for (let i = 0; i < this.xs.length; i++) {
      const x = this.xs[i], y = this.ty(this.ys[i]);
      if (isFinite(x)) { if (x < x0) x0 = x; if (x > x1) x1 = x; }
      if (isFinite(y)) { if (y < y0) y0 = y; if (y > y1) y1 = y; }
    }
    if (!isFinite(x0)) { x0 = 0; x1 = 1; } if (!isFinite(y0)) { y0 = 0; y1 = 1; }
    if (x0 === x1) { x0 -= .5; x1 += .5; }
    if (y0 === y1) { y0 -= .5; y1 += .5; }
    const pad = (y1 - y0) * .05;
    this.view = { x0, x1, y0: y0 - pad, y1: y1 + pad };
  }
  size() {
    const dpr = window.devicePixelRatio || 1, w = this.el.clientWidth || 600, H = this.height;
    for (const c of [this.canvas, this.over]) { c.width = w * dpr; c.height = H * dpr; c.style.width = w + 'px'; c.style.height = H + 'px'; }
    this.dpr = dpr; this.W = w; this.H = H;
  }
  sx(x) { const { x0, x1 } = this.view; return this.m.l + (x - x0) / (x1 - x0) * (this.W - this.m.l - this.m.r); }
  sy(y) { const { y0, y1 } = this.view; return this.H - this.m.b - (y - y0) / (y1 - y0) * (this.H - this.m.t - this.m.b); }
  ix(px) { const { x0, x1 } = this.view; return x0 + (px - this.m.l) / (this.W - this.m.l - this.m.r) * (x1 - x0); }
  iy(py) { const { y0, y1 } = this.view; return y0 + (this.H - this.m.b - py) / (this.H - this.m.t - this.m.b) * (y1 - y0); }
  draw() {
    if (!this.view) return;
    this.size();
    const c = this.canvas.getContext('2d'); c.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    const { l, r, t, b } = this.m, W = this.W, H = this.H, v = this.view;
    const grid = cssVar('--plot-grid'), axis = cssVar('--plot-axis'), line = cssVar('--plot-line');
    c.clearRect(0, 0, W, H); c.font = '11px ' + cssVar('--mono'); c.fillStyle = axis; c.strokeStyle = grid; c.lineWidth = 1;
    // grid + ticks
    c.textAlign = 'center'; c.textBaseline = 'top';
    for (const x of niceTicks(v.x0, v.x1, Math.max(2, Math.floor((W - l - r) / 90)))) {
      const px = Math.round(this.sx(x)) + .5; c.beginPath(); c.moveTo(px, t); c.lineTo(px, H - b); c.stroke();
      c.fillText(fmt(x), px, H - b + 6);
    }
    c.textAlign = 'right'; c.textBaseline = 'middle';
    for (const y of niceTicks(v.y0, v.y1, Math.max(2, Math.floor((H - t - b) / 45)))) {
      const py = Math.round(this.sy(y)) + .5; c.beginPath(); c.moveTo(l, py); c.lineTo(W - r, py); c.stroke();
      c.fillText(this.opt.logY ? fmt(Math.pow(10, y)) : fmt(y), l - 6, py);
    }
    c.strokeStyle = axis; c.strokeRect(l + .5, t + .5, W - l - r, H - t - b);
    // labels
    c.textBaseline = 'alphabetic'; c.textAlign = 'center';
    if (this.opt.xlabel) c.fillText(this.opt.xlabel, l + (W - l - r) / 2, H - 4);
    if (this.opt.ylabel) { c.save(); c.translate(12, t + (H - t - b) / 2); c.rotate(-Math.PI / 2); c.fillText(this.opt.ylabel, 0, 0); c.restore(); }
    // data
    c.save(); c.beginPath(); c.rect(l, t, W - l - r, H - t - b); c.clip();
    c.strokeStyle = line; c.fillStyle = line; c.lineWidth = 1.4; c.lineJoin = 'round';
    const n = this.xs.length, pw = W - l - r;
    let i0 = 0, i1 = n - 1;                           // visible index range when x is monotone
    if (n > 2 && this.xs[0] <= this.xs[n - 1]) {
      i0 = Math.max(0, this.lowerBound(v.x0) - 1); i1 = Math.min(n - 1, this.lowerBound(v.x1) + 1);
    }
    if (i1 - i0 > 4 * pw) {                           // min/max decimation
      const mn = new Float64Array(pw + 1).fill(Infinity), mx = new Float64Array(pw + 1).fill(-Infinity);
      for (let i = i0; i <= i1; i++) {
        const y = this.ty(this.ys[i]); if (!isFinite(y)) continue;
        const col = Math.floor(this.sx(this.xs[i]) - l); if (col < 0 || col > pw) continue;
        if (y < mn[col]) mn[col] = y; if (y > mx[col]) mx[col] = y;
      }
      c.beginPath();
      for (let col = 0; col <= pw; col++) if (mx[col] >= mn[col]) {
        c.moveTo(l + col + .5, this.sy(mn[col])); c.lineTo(l + col + .5, this.sy(mx[col]));
      }
      c.stroke();
    } else {
      c.beginPath(); let pen = false;
      for (let i = i0; i <= i1; i++) {
        const y = this.ty(this.ys[i]);
        if (!isFinite(y) || !isFinite(this.xs[i])) { pen = false; continue; }
        const px = this.sx(this.xs[i]), py = this.sy(y);
        pen ? c.lineTo(px, py) : c.moveTo(px, py); pen = true;
      }
      c.stroke();
      if (i1 - i0 < pw / 6) for (let i = i0; i <= i1; i++) {
        const y = this.ty(this.ys[i]); if (!isFinite(y)) continue;
        c.beginPath(); c.arc(this.sx(this.xs[i]), this.sy(y), 2.2, 0, 6.3); c.fill();
      }
    }
    c.restore();
  }
  lowerBound(x) { let lo = 0, hi = this.xs.length; while (lo < hi) { const mid = (lo + hi) >> 1; this.xs[mid] < x ? lo = mid + 1 : hi = mid; } return lo; }
  clearOver() { const c = this.over.getContext('2d'); c.setTransform(1, 0, 0, 1, 0, 0); c.clearRect(0, 0, this.over.width, this.over.height); }
  pos(e) { const r = this.over.getBoundingClientRect(); return [e.clientX - r.left, e.clientY - r.top]; }
  hover(e) {
    if (this.dragging || !this.view) return;
    const [mx] = this.pos(e); const { l, r, t, b } = this.m;
    if (mx < l || mx > this.W - r) { this.clearOver(); this.readout.classList.add('hidden'); return; }
    let best = -1, bd = Infinity;
    const x = this.ix(mx);
    if (this.xs.length > 2 && this.xs[0] <= this.xs[this.xs.length - 1]) {          // monotone: binary search
      const k = this.lowerBound(x);
      for (const i of [k - 1, k]) if (i >= 0 && i < this.xs.length && isFinite(this.ty(this.ys[i]))) { const d = Math.abs(this.xs[i] - x); if (d < bd) { bd = d; best = i; } }
    } else for (let i = 0; i < this.xs.length; i++) { const d = Math.abs(this.xs[i] - x); if (d < bd && isFinite(this.ty(this.ys[i]))) { bd = d; best = i; } }
    if (best < 0) return;
    const c = this.over.getContext('2d'); c.setTransform(this.dpr, 0, 0, this.dpr, 0, 0); c.clearRect(0, 0, this.W, this.H);
    const px = this.sx(this.xs[best]), py = this.sy(this.ty(this.ys[best]));
    c.strokeStyle = cssVar('--plot-axis'); c.setLineDash([3, 3]); c.beginPath(); c.moveTo(px, t); c.lineTo(px, this.H - b); c.stroke(); c.setLineDash([]);
    c.fillStyle = cssVar('--accent'); c.beginPath(); c.arc(px, py, 4, 0, 6.3); c.fill();
    this.readout.textContent = `${this.opt.xname || 'x'}=${fmt(this.xs[best])}  ${this.opt.yname || 'y'}=${fmt(this.ys[best])}${this.opt.idxOffset != null ? '  [#' + (best + this.opt.idxOffset) + ']' : ''}`;
    this.readout.classList.remove('hidden');
  }
  dragStart(e) {
    const [x0, y0] = this.pos(e); this.dragging = true;
    const mv = ev => {
      const [x1, y1] = this.pos(ev); const c = this.over.getContext('2d');
      c.setTransform(this.dpr, 0, 0, this.dpr, 0, 0); c.clearRect(0, 0, this.W, this.H);
      c.fillStyle = 'rgba(31,111,235,.15)'; c.strokeStyle = cssVar('--accent');
      c.fillRect(Math.min(x0, x1), Math.min(y0, y1), Math.abs(x1 - x0), Math.abs(y1 - y0));
      c.strokeRect(Math.min(x0, x1), Math.min(y0, y1), Math.abs(x1 - x0), Math.abs(y1 - y0));
    };
    const up = ev => {
      window.removeEventListener('mousemove', mv); window.removeEventListener('mouseup', up);
      this.dragging = false; const [x1, y1] = this.pos(ev);
      if (Math.abs(x1 - x0) > 6 && Math.abs(y1 - y0) > 6) {
        const a = this.ix(Math.min(x0, x1)), bb = this.ix(Math.max(x0, x1)), yb = this.iy(Math.min(y0, y1)), ya = this.iy(Math.max(y0, y1));
        this.view = { x0: a, x1: bb, y0: ya, y1: yb }; this.draw();
      }
      this.clearOver();
    };
    window.addEventListener('mousemove', mv); window.addEventListener('mouseup', up);
  }
}

const VIRIDIS = ['#440154', '#472d7b', '#3b528b', '#2c728e', '#21908d', '#27ad81', '#5cc863', '#aadc32', '#fde725']
  .map(s => [1, 3, 5].map(i => parseInt(s.slice(i, i + 2), 16)));
function colorAt(t) {
  t = Math.max(0, Math.min(1, t)) * (VIRIDIS.length - 1);
  const i = Math.min(VIRIDIS.length - 2, Math.floor(t)), f = t - i, a = VIRIDIS[i], b = VIRIDIS[i + 1];
  return [a[0] + (b[0] - a[0]) * f, a[1] + (b[1] - a[1]) * f, a[2] + (b[2] - a[2]) * f];
}

class Heatmap {
  constructor(height = 420) {
    this.height = height;
    this.canvas = h('canvas'); this.readout = h('div', { class: 'readout hidden' });
    this.el = h('div', { class: 'plotwrap' }, this.canvas, this.readout);
    this.bar = h('div', { class: 'colorbar' }); this.barLbl = h('div', { class: 'hint', style: 'display:flex;justify-content:space-between' });
    this.m = { l: 64, r: 14, t: 10, b: 30 };
    new ResizeObserver(() => this.draw()).observe(this.el);
    this.canvas.addEventListener('mousemove', e => this.hover(e));
    this.canvas.addEventListener('mouseleave', () => this.readout.classList.add('hidden'));
  }
  set(values, rows, cols, opt) {
    this.vals = values; this.rows = rows; this.cols = cols; this.opt = opt || {};
    const arr = new Float64Array(values.length); let k = 0;
    const f = this.opt.log ? (v => v > 0 ? Math.log10(v) : NaN) : (v => v);
    for (let i = 0; i < values.length; i++) { const v = f(values[i]); arr[i] = v; if (isFinite(v)) k++; }
    let lo = Infinity, hi = -Infinity;
    if (this.opt.robust && k > 20) {
      const s = Float64Array.from(arr.filter(isFinite)).sort(); lo = s[Math.floor(s.length * .01)]; hi = s[Math.min(s.length - 1, Math.floor(s.length * .99))];
    } else for (const v of arr) if (isFinite(v)) { if (v < lo) lo = v; if (v > hi) hi = v; }
    if (!isFinite(lo)) { lo = 0; hi = 1; } if (hi === lo) hi = lo + 1;
    this.lo = lo; this.hi = hi;
    const off = document.createElement('canvas'); off.width = cols; off.height = rows;
    const img = off.getContext('2d').createImageData(cols, rows);
    for (let i = 0; i < arr.length; i++) {
      const v = arr[i], o = i * 4;
      if (!isFinite(v)) { img.data[o + 3] = 0; continue; }
      const [r, g, b] = colorAt((v - lo) / (hi - lo));
      img.data[o] = r; img.data[o + 1] = g; img.data[o + 2] = b; img.data[o + 3] = 255;
    }
    off.getContext('2d').putImageData(img, 0, 0); this.off = off;
    const unlog = x => this.opt.log ? Math.pow(10, x) : x;
    this.bar.style.background = `linear-gradient(to right, ${VIRIDIS.map(c => `rgb(${c})`).join(',')})`;
    this.barLbl.replaceChildren(h('span', null, fmt(unlog(lo)) + (this.opt.robust ? ' (1%)' : '')), h('span', null, fmt(unlog(hi)) + (this.opt.robust ? ' (99%)' : '')));
    this.draw();
  }
  draw() {
    if (!this.off) return;
    const dpr = window.devicePixelRatio || 1, W = this.el.clientWidth || 600, H = this.height;
    this.canvas.width = W * dpr; this.canvas.height = H * dpr; this.canvas.style.width = W + 'px'; this.canvas.style.height = H + 'px';
    this.W = W; this.H = H; const c = this.canvas.getContext('2d'); c.setTransform(dpr, 0, 0, dpr, 0, 0);
    const { l, r, t, b } = this.m; c.imageSmoothingEnabled = false;
    c.drawImage(this.off, l, t, W - l - r, H - t - b);
    const axis = cssVar('--plot-axis'); c.fillStyle = axis; c.strokeStyle = axis; c.font = '11px ' + cssVar('--mono'); c.strokeRect(l + .5, t + .5, W - l - r, H - t - b);
    const xs = this.opt.xs, r0 = this.opt.row0 || 0;
    c.textAlign = 'center'; c.textBaseline = 'top';
    for (const x of niceTicks(0, this.cols - 1, Math.max(2, Math.floor((W - l - r) / 90)))) {
      const px = l + (x + .5) / this.cols * (W - l - r); c.fillRect(Math.round(px), H - b, 1, 4);
      c.fillText(xs && xs[Math.round(x)] != null ? fmt(xs[Math.round(x)]) : fmt(x), px, H - b + 6);
    }
    c.textAlign = 'right'; c.textBaseline = 'middle';
    for (const y of niceTicks(0, this.rows - 1, Math.max(2, Math.floor((H - t - b) / 40)))) {
      const py = t + (y + .5) / this.rows * (H - t - b); c.fillRect(l - 4, Math.round(py), 4, 1); c.fillText(fmt(r0 + y), l - 7, py);
    }
  }
  hover(e) {
    const rc = this.canvas.getBoundingClientRect(), x = e.clientX - rc.left, y = e.clientY - rc.top, { l, r, t, b } = this.m;
    const ci = Math.floor((x - l) / (this.W - l - r) * this.cols), ri = Math.floor((y - t) / (this.H - t - b) * this.rows);
    if (ci < 0 || ri < 0 || ci >= this.cols || ri >= this.rows) { this.readout.classList.add('hidden'); return; }
    const xs = this.opt.xs, xv = xs && xs[ci] != null ? `  x=${fmt(xs[ci])}` : '';
    this.readout.textContent = `row ${(this.opt.row0 || 0) + ri}, col ${ci}${xv}  value=${fmt(this.vals[ri * this.cols + ci])}`;
    this.readout.classList.remove('hidden');
  }
}

/* ------------------------------------------------------------ detail: field */
async function showField(n) {
  const token = ++S.fieldToken, d = $('#detail');
  d.replaceChildren(h('div', { class: 'hint' }, 'Loading…'));
  const shape = n.shape || [], total = shape.length ? prod(shape) : 1;
  const rowLen = shape.length > 1 ? shape[shape.length - 1] : total, nrows = shape.length > 1 ? total / rowLen : 1;
  const F = { n, shape, total, rowLen, nrows, nd: shape.length > 1, scalar: shape.length === 0, token,
              mode: 'plot', row: 0, offset: 0, limit: Math.min(total, MAX_PLOT_POINTS), xsel: '', logY: false, shown: 500,
              hmRow0: 0, hmRows: Math.max(1, Math.min(nrows, Math.floor(HEATMAP_ELEMS / Math.max(1, rowLen)))), hmLog: false, hmRobust: true };
  try {
    if (F.scalar) {
      const r = await api('/field', { key: S.key, path: n.path });
      if (token !== S.fieldToken) return;
      F.meta = r; return renderScalar(F);
    }
    await fetchWindow(F);
    if (token !== S.fieldToken) return;
    renderArray(F);
  } catch (e) { if (token === S.fieldToken) d.replaceChildren(crumbs(n), h('div', { class: 'err' }, e.message)); }
}

function header(F, r) {
  const n = F.n, meta = F.meta || r || {};
  const unit = meta.units || n.units;
  return [
    crumbs(n), h('h2', null, n.name),
    h('div', { class: 'badges' },
      h('span', { class: 'badge' }, meta.dtype || n.dtype || '?'),
      h('span', { class: 'badge' }, shapeStr(F.shape)),
      unit ? h('span', { class: 'badge' }, 'units: ' + unit) : null,
      F.scalar ? null : h('span', { class: 'badge' }, `${F.total.toLocaleString()} elements`),
      h('button', { class: 'btn small', onclick: () => { const u = apiUrl('/field', { key: S.key, path: n.path, offset: F.offset, limit: F.limit }); navigator.clipboard && navigator.clipboard.writeText(u); status('API URL copied'); } }, 'Copy API URL')),
    h('h3', null, 'Attributes'), attrsTable(meta.attrs),
  ];
}

function renderScalar(F) {
  const v = F.meta.values, p = prettyScalar(Array.isArray(v) && v.length === 1 ? v[0] : v);
  $('#detail').replaceChildren(...header(F), h('h3', null, 'Value' + (p.json ? ' (JSON)' : '')),
    h('pre', { class: 'val' }, p.text),
    h('div', { class: 'toolbar' }, h('button', { class: 'btn small', onclick: () => { navigator.clipboard && navigator.clipboard.writeText(p.text); status('Copied'); } }, 'Copy value')));
}

/* fetch whichever window the current mode needs */
async function fetchWindow(F) {
  const common = { key: S.key, path: F.n.path };
  if (F.mode === 'heatmap') {
    const off = F.hmRow0 * F.rowLen, lim = F.hmRows * F.rowLen;
    F.hm = await api('/field', { ...common, offset: off, limit: lim });
    if (!F.meta) F.meta = F.hm;
  } else if (F.nd) {
    F.data = await api('/field', { ...common, offset: F.row * F.rowLen, limit: F.rowLen });
    if (!F.meta) F.meta = F.data;
  } else {
    F.data = await api('/field', { ...common, offset: F.offset, limit: F.limit });
    if (!F.meta) F.meta = F.data;
  }
  await fetchX(F);
}
function xCandidates(F) {
  const p = F.n.parent; if (!p) return [];
  return p.children.filter(c => !isGroup(c) && c !== F.n && c.shape && c.shape.length === 1 && c.shape[0] === F.rowLen && c.dtype !== 'string');
}
async function fetchX(F) {
  F.xs = null;
  if (!F.xsel) return;
  const c = xCandidates(F).find(c => c.name === F.xsel); if (!c) return;
  let off = 0, lim = F.rowLen;
  if (!F.nd) { off = F.offset; lim = F.limit; }
  const r = await api('/field', { key: S.key, path: c.path, offset: off, limit: lim });
  if (Array.isArray(r.values) && r.values.every(isNum)) F.xs = r.values;
}

function renderArray(F) {
  const numeric = F.meta && Array.isArray((F.data || F.hm || {}).values) && (F.data || F.hm).values.every(isNum);
  F.numeric = numeric || (F.mode === 'heatmap');
  if (!numeric && F.mode !== 'table') F.mode = 'table';
  const modes = [];
  if (numeric) modes.push([F.nd ? 'plot' : 'plot', F.nd ? 'Row plot' : 'Plot']);
  if (numeric && F.nd) modes.push(['heatmap', 'Heatmap']);
  modes.push(['table', 'Table']);
  const seg = h('div', { class: 'seg' }, ...modes.map(([m, label]) =>
    h('button', { class: F.mode === m ? 'on' : '', onclick: async () => { F.mode = m; await refresh(F); } }, label)));

  const body = h('div');
  if (F.mode === 'heatmap') buildHeatmap(F, body);
  else if (F.mode === 'plot') buildPlot(F, body);
  else buildTable(F, body);

  const ctl = [h('h3', null, 'Data'), h('div', { class: 'toolbar' }, seg)];
  $('#detail').replaceChildren(...header(F), ...ctl, body);
}
async function refresh(F) {
  const token = F.token;
  try {
    // table/plot share data; heatmap has its own block
    if ((F.mode === 'heatmap' && !F.hm) || (F.mode !== 'heatmap' && !F.data)) await fetchWindow(F); else await fetchX(F);
    if (token !== S.fieldToken) return; renderArray(F);
  } catch (e) { status(e.message, true); }
}
async function reload(F) {
  const token = F.token;
  try { status('Loading…', false, true); await fetchWindow(F); if (token !== S.fieldToken) return; hideStatus(); renderArray(F); }
  catch (e) { status(e.message, true); }
}

function windowControls(F) {
  if (F.nd) {
    const slider = h('input', { type: 'range', min: 0, max: Math.max(0, F.nrows - 1), value: F.row, style: 'flex:1;min-width:140px' });
    const num = h('input', { type: 'number', min: 0, max: F.nrows - 1, value: F.row });
    let t; const go = v => { v = Math.max(0, Math.min(F.nrows - 1, Math.round(+v) || 0)); F.row = v; slider.value = v; num.value = v; clearTimeout(t); t = setTimeout(() => { F.data = null; reload(F); }, 160); };
    slider.addEventListener('input', () => go(slider.value)); num.addEventListener('change', () => go(num.value));
    return h('div', { class: 'toolbar' },
      h('label', null, F.shape.length > 2 ? 'row (flattened leading dims)' : 'row'), h('button', { class: 'btn small', onclick: () => go(F.row - 1) }, '◀'), num,
      h('button', { class: 'btn small', onclick: () => go(F.row + 1) }, '▶'), slider, h('span', { class: 'hint' }, `of ${F.nrows.toLocaleString()} rows × ${F.rowLen} cols`));
  }
  const off = h('input', { type: 'number', min: 0, max: F.total - 1, value: F.offset }), lim = h('input', { type: 'number', min: 1, value: F.limit });
  const load = (o, l) => { F.offset = Math.max(0, Math.min(F.total - 1, o)); F.limit = Math.max(1, l); F.data = null; reload(F); };
  return h('div', { class: 'toolbar' },
    h('label', null, 'offset', off), h('label', null, 'limit', lim),
    h('button', { class: 'btn small', onclick: () => load(+off.value, +lim.value) }, 'Load'),
    h('button', { class: 'btn small', disabled: F.offset === 0, onclick: () => load(F.offset - F.limit, F.limit) }, '◀ prev'),
    h('button', { class: 'btn small', disabled: F.offset + F.limit >= F.total, onclick: () => load(F.offset + F.limit, F.limit) }, 'next ▶'),
    h('span', { class: 'hint' }, `showing ${F.data.offset ?? F.offset}–${(F.data.offset ?? F.offset) + (F.data.count ?? 0) - 1} of ${F.total.toLocaleString()}`));
}

function xSelect(F) {
  const c = xCandidates(F); if (!c.length) return null;
  const sel = h('select', { onchange: async () => { F.xsel = sel.value; await refresh(F); } },
    h('option', { value: '' }, 'index'), ...c.map(x => h('option', { value: x.name, selected: x.name === F.xsel }, x.name + (x.units ? ` (${x.units})` : ''))));
  return h('label', null, 'x axis', sel);
}

function exportButtons(F, values, name, cols) {
  const base = F.n.name + name;
  return [
    h('button', { class: 'btn small', onclick: () => download(base + '.csv', toCSV(values, cols), 'text/csv') }, 'Download CSV'),
    h('button', { class: 'btn small', onclick: () => download(base + '.json', JSON.stringify(values), 'application/json') }, 'Download JSON'),
  ];
}
function toCSV(values, cols) {
  if (!cols || cols < 2) return 'index,value\n' + values.map((v, i) => `${i},${csvCell(v)}`).join('\n');
  const out = []; for (let i = 0; i < values.length; i += cols) out.push(values.slice(i, i + cols).map(csvCell).join(','));
  return out.join('\n');
}
const csvCell = v => typeof v === 'string' ? '"' + v.replace(/"/g, '""') + '"' : v;

function buildPlot(F, body) {
  const vals = F.data.values, plot = new LinePlot(360);
  const unit = F.meta.units || F.n.units;
  const xcand = xSelect(F);
  const log = h('label', null, h('input', { type: 'checkbox', checked: F.logY, onchange: e => { F.logY = e.target.checked; draw(); } }), 'log y');
  const base = F.nd ? 0 : (F.data.offset || 0);
  const draw = () => {
    const xs = F.xs || vals.map((_, i) => i + (F.nd ? 0 : base));
    const xc = F.xsel ? xCandidates(F).find(c => c.name === F.xsel) : null;
    plot.set(xs, vals, { logY: F.logY, xlabel: F.xsel ? F.xsel + (xc && xc.units ? ` (${xc.units})` : '') : (F.nd ? 'column index' : 'index'),
      ylabel: F.n.name + (unit ? ` (${unit})` : ''), xname: F.xsel || 'idx', yname: F.n.name, idxOffset: base });
  };
  body.append(windowControls(F), h('div', { class: 'toolbar' }, xcand, log,
    h('span', { class: 'hint' }, 'drag to zoom · double-click to reset'), ...exportButtons(F, vals, F.nd ? `_row${F.row}` : `_${F.data.offset || 0}`)), plot.el);
  requestAnimationFrame(draw);
}

function buildHeatmap(F, body) {
  const hm = new Heatmap(420), vals = F.hm.values, rows = Math.round(vals.length / F.rowLen);
  const r0 = h('input', { type: 'number', min: 0, max: F.nrows - 1, value: F.hmRow0 }), nr = h('input', { type: 'number', min: 1, max: F.nrows, value: F.hmRows });
  const go = () => {
    F.hmRow0 = Math.max(0, Math.min(F.nrows - 1, +r0.value || 0));
    F.hmRows = Math.max(1, Math.min(F.nrows - F.hmRow0, Math.floor(HEATMAP_MAX / F.rowLen), +nr.value || 1));
    F.hm = null; reload(F);
  };
  const xcand = xSelect(F);
  const redraw = () => hm.set(vals, rows, F.rowLen, { log: F.hmLog, robust: F.hmRobust, row0: F.hmRow0, xs: F.xs });
  body.append(h('div', { class: 'toolbar' }, h('label', null, 'first row', r0), h('label', null, 'rows', nr),
      h('button', { class: 'btn small', onclick: go }, 'Load'),
      h('button', { class: 'btn small', disabled: F.hmRow0 === 0, onclick: () => { r0.value = Math.max(0, F.hmRow0 - F.hmRows); go(); } }, '◀ prev'),
      h('button', { class: 'btn small', disabled: F.hmRow0 + F.hmRows >= F.nrows, onclick: () => { r0.value = F.hmRow0 + F.hmRows; go(); } }, 'next ▶'),
      h('span', { class: 'hint' }, `rows ${F.hmRow0}–${F.hmRow0 + rows - 1} of ${F.nrows.toLocaleString()}`)),
    h('div', { class: 'toolbar' }, xcand,
      h('label', null, h('input', { type: 'checkbox', checked: F.hmLog, onchange: e => { F.hmLog = e.target.checked; redraw(); } }), 'log color'),
      h('label', null, h('input', { type: 'checkbox', checked: F.hmRobust, onchange: e => { F.hmRobust = e.target.checked; redraw(); } }), 'clip 1–99%'),
      ...exportButtons(F, vals, `_rows${F.hmRow0}-${F.hmRow0 + rows - 1}`, F.rowLen)),
    hm.el, hm.bar, hm.barLbl);
  requestAnimationFrame(redraw);
}

function buildTable(F, body) {
  const data = F.mode === 'heatmap' ? F.hm : F.data, vals = data.values, base = data.offset || 0;
  const rowLen = F.nd ? F.rowLen : 0;
  const render = () => {
    const shown = Math.min(vals.length, F.shown);
    const cols = ['#'].concat(F.nd ? ['[row, col]'] : [], 'value');
    const trs = []; for (let i = 0; i < shown; i++) {
      const g = base + i, cells = [h('td', { class: 'num' }, g)];
      if (F.nd) cells.push(h('td', { class: 'num' }, `[${Math.floor(g / rowLen)}, ${g % rowLen}]`));
      cells.push(h('td', { class: 'num' }, isNum(vals[i]) ? fmt(vals[i]) : String(vals[i]))); trs.push(h('tr', null, cells));
    }
    tbl.replaceChildren(h('div', { class: 'scroll' }, h('table', { class: 'grid' }, h('thead', null, h('tr', null, cols.map(c => h('th', null, c)))), h('tbody', null, trs))),
      shown < vals.length ? h('div', { class: 'toolbar' }, h('button', { class: 'btn small', onclick: () => { F.shown += 500; render(); } }, `Show more (${shown} / ${vals.length})`)) : null);
  };
  const tbl = h('div');
  body.append(F.mode === 'heatmap' ? null : windowControls(F), h('div', { class: 'toolbar' }, ...exportButtons(F, vals, `_${base}`, F.nd ? rowLen : 0)), tbl);
  render();
}

/* ------------------------------------------------------------ raw tab / tabs */
async function setTab(t) {
  S.tab = t;
  for (const b of document.querySelectorAll('#tabs button')) b.classList.toggle('active', b.dataset.tab === t);
  $('#detail').classList.toggle('hidden', t !== 'detail'); $('#raw').classList.toggle('hidden', t !== 'raw');
  if (t === 'raw' && S.key) {
    const raw = $('#raw');
    try {
      if (S.rawCache[S.key] == null) { raw.replaceChildren(h('div', { class: 'hint' }, 'Loading…')); S.rawCache[S.key] = await api('/tree', { key: S.key }); }
      raw.replaceChildren(h('div', { class: 'toolbar' }, h('button', { class: 'btn small', onclick: () => { navigator.clipboard && navigator.clipboard.writeText(S.rawCache[S.key]); status('Copied'); } }, 'Copy'),
        h('button', { class: 'btn small', onclick: () => download(S.key.split('/').pop() + '.tree.txt', S.rawCache[S.key], 'text/plain') }, 'Download')),
        h('pre', { class: 'val raw' }, S.rawCache[S.key]));
    } catch (e) { raw.replaceChildren(h('div', { class: 'err' }, e.message)); }
  }
}
$('#tabs').addEventListener('click', e => { if (e.target.dataset.tab) setTab(e.target.dataset.tab); });

/* ------------------------------------------------------------ open file */
async function openFile(key, path) {
  key = key.trim(); if (!key) return;
  S.key = key; S.nodes.clear(); S.selected = null; S.fieldToken++;
  $('#fileInput').value = key;
  const tree = $('#tree'); tree.replaceChildren(h('div', { class: 'hint', style: 'padding:8px' }, 'Loading…'));
  $('#detail').replaceChildren();
  try {
    const g = await api('/group', { key });
    tree.replaceChildren();
    const root = makeNode(null, { name: '', type: 'group', class: g.class }, '');
    root.attrs = g.attrs || {}; S.root = root; renderNode(root, tree);
    // children come from the response we already have
    for (const c of (g.children || [])) { const cn = makeNode(root, c, c.name); root.children.push(cn); renderNode(cn, root.kidsEl); }
    root.loaded = true; await toggle(root, true);
    if (path) await revealPath(path); else select(root, true);
    updateHash();
    if (S.tab === 'raw') setTab('raw');
  } catch (e) {
    S.root = null; tree.replaceChildren(h('div', { class: 'err', style: 'padding:8px' }, e.message));
    $('#detail').replaceChildren(h('div', { class: 'err' }, e.message));
  }
}

async function loadFiles() {
  try {
    const r = await api('/files');
    $('#fileList').replaceChildren(...(r.files || []).map(f => h('option', { value: f.key }, `${fmtBytes(f.size)}`)));
    return r.files || [];
  } catch (e) { status('Could not list files: ' + e.message, true); return []; }
}

/* ------------------------------------------------------------ wiring */
$('#loadBtn').addEventListener('click', () => openFile($('#fileInput').value));
$('#fileInput').addEventListener('keydown', e => { if (e.key === 'Enter') openFile(e.target.value); });
$('#fileInput').addEventListener('change', e => { if ([...$('#fileList').options].some(o => o.value === e.target.value)) openFile(e.target.value); });
$('#reloadFiles').addEventListener('click', loadFiles);
$('#filterInput').addEventListener('input', applyFilter);
$('#expandAll').addEventListener('click', expandAll);
$('#collapseAll').addEventListener('click', collapseAll);
$('#themeBtn').addEventListener('click', () => {
  const t = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = t; try { localStorage.setItem('nxtheme', t); } catch (_) {}
  if (S.selected && !isGroup(S.selected)) S.selected.row.click();
});
(() => {
  let t; try { t = localStorage.getItem('nxtheme'); } catch (_) {}
  if (!t) t = matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  document.documentElement.dataset.theme = t;
})();
(() => {   // resizable sidebar
  const sp = $('#splitter'), sb = $('.sidebar');
  sp.addEventListener('mousedown', e => {
    e.preventDefault(); sp.classList.add('drag');
    const mv = ev => { sb.style.width = Math.max(180, Math.min(innerWidth - 300, ev.clientX)) + 'px'; };
    const up = () => { sp.classList.remove('drag'); removeEventListener('mousemove', mv); removeEventListener('mouseup', up); };
    addEventListener('mousemove', mv); addEventListener('mouseup', up);
  });
})();

addEventListener('hashchange', () => {
  const hp = new URLSearchParams(location.hash.slice(1)), key = hp.get('key') || '', path = hp.get('path') || '';
  if (!key) return;
  if (key !== S.key) openFile(key, path);
  else if (!S.selected || S.selected.path !== path) revealPath(path);
});

(async () => {
  const files = await loadFiles();
  const hp = new URLSearchParams(location.hash.slice(1));
  const key = hp.get('key') || (files.length === 1 ? files[0].key : '');
  if (key) openFile(key, hp.get('path') || '');
  else $('#detail').replaceChildren(h('div', { class: 'empty' }, files.length ? 'Choose a file above to browse its NeXus tree.' : 'No files found. Type a file key above and press Open.'));
})();
})();
