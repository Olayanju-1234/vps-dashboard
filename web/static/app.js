// vps-dashboard frontend. Vanilla JS, no build step. Polls /api/state every
// 10 s and re-renders the current view; every string from the API is escaped.
(() => {
  'use strict';

  const REFRESH_MS = 10000;
  const view = document.getElementById('view');
  const tooltip = document.getElementById('tooltip');

  // ---------- helpers ----------
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const zeroTime = (t) => !t || t.startsWith('0001-');
  const ms = (t) => (zeroTime(t) ? 0 : Date.parse(t));

  function fmtBytes(n) {
    if (!n) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return `${n >= 100 || i === 0 ? Math.round(n) : n.toFixed(1)} ${u[i]}`;
  }
  const fmtPct = (v, d = 1) => `${(v ?? 0).toFixed(d)}%`;
  const fmtNum = (n) => (n ?? 0).toLocaleString('en-GB');

  function fmtDur(sec) {
    sec = Math.max(0, Math.round(sec));
    if (sec < 60) return `${sec}s`;
    if (sec < 3600) return `${Math.floor(sec / 60)}m`;
    if (sec < 172800) { const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60); return m && h < 10 ? `${h}h ${m}m` : `${h}h`; }
    return `${Math.floor(sec / 86400)}d`;
  }
  function ago(t) {
    if (zeroTime(t)) return '—';
    const d = (Date.now() - ms(t)) / 1000;
    return d < 0 ? `in ${fmtDur(-d)}` : `${fmtDur(d)} ago`;
  }
  function until(t) {
    if (zeroTime(t)) return '—';
    const d = (ms(t) - Date.now()) / 1000;
    return d < 0 ? `${fmtDur(-d)} ago` : `in ${fmtDur(d)}`;
  }
  function fmtDate(t) {
    if (zeroTime(t)) return '—';
    return new Date(t).toLocaleString('en-GB', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone: 'UTC' }) + ' UTC';
  }
  const short = (sha) => (sha ? esc(sha.slice(0, 7)) : '—');

  const ICONS = {
    good: '<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="7" fill="currentColor"/><path d="M4.8 8.3l2.1 2.1 4.3-4.6" fill="none" stroke="#fff" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    warning: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 1.6l7 12.4H1z" fill="currentColor" stroke="currentColor" stroke-width="1.2" stroke-linejoin="round"/><path d="M8 6v3.6" stroke="#1a1a19" stroke-width="1.8" stroke-linecap="round"/><circle cx="8" cy="11.9" r="1" fill="#1a1a19"/></svg>',
    critical: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5.1 1h5.8L15 5.1v5.8L10.9 15H5.1L1 10.9V5.1z" fill="currentColor"/><path d="M5.6 5.6l4.8 4.8M10.4 5.6l-4.8 4.8" stroke="#fff" stroke-width="1.8" stroke-linecap="round"/></svg>',
    info: '<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="7" fill="currentColor" opacity=".55"/><path d="M8 7.2v4" stroke="#fff" stroke-width="1.8" stroke-linecap="round"/><circle cx="8" cy="4.8" r="1" fill="#fff"/></svg>',
  };
  const badge = (level, label) => `<span class="badge ${level}">${ICONS[level] || ''}${esc(label)}</span>`;
  const plain = (label) => `<span class="badge plain">${esc(label)}</span>`;

  const PROJECT_STATUS = { ok: ['good', 'Healthy'], warn: ['warning', 'Needs a look'], critical: ['critical', 'Problem'] };
  const projectBadge = (s) => badge(...(PROJECT_STATUS[s] || ['info', s || 'Unknown']));

  function unitBadge(u) {
    if (u.load !== 'loaded') return badge('critical', u.load);
    if (u.active === 'failed') return badge('critical', 'failed');
    if (u.active !== 'active') return badge('critical', u.active);
    if (u.sub === 'waiting') return badge('good', 'scheduled');
    return badge('good', u.sub || 'active');
  }
  function deployBadge(d) {
    if (!d || !d.method || d.method === 'none') return plain('not tracked');
    switch (d.status) {
      case 'current': return badge('good', 'matches main');
      case 'behind': return badge('warning', `${d.behind} behind main`);
      case 'error': return badge('info', 'unknown');
      default: return badge('info', d.main_source === 'local-ref' ? 'differs from origin' : 'unknown');
    }
  }
  const BACKUP = { ok: ['good', 'fresh'], stale: ['warning', 'stale'], missing: ['critical', 'missing'], error: ['info', 'unreadable'] };
  const backupBadge = (b) => badge(...(BACKUP[b.status] || ['info', b.status]));
  const PORT = { ok: ['good', 'ok'], exposed: ['critical', 'exposed'], down: ['info', 'not listening'], unlisted: ['info', 'not in map'], unexpected: ['critical', 'unknown public'] };
  const portBadge = (p) => badge(...(PORT[p.status] || ['info', p.status]));

  function meterClass(pct) { return pct >= 90 ? 'crit' : pct >= 75 ? 'warn' : ''; }
  const meter = (pct, extra = '') => `<div class="meter ${meterClass(pct)} ${extra}"><span style="width:${Math.min(100, Math.max(0, pct)).toFixed(1)}%"></span></div>`;

  // ---------- tiny SVG charts ----------
  function sparkline(values, color, { w = 240, h = 40, max = null } = {}) {
    if (!values || values.length < 2) return `<svg class="spark" viewBox="0 0 ${w} ${h}" aria-hidden="true"></svg>`;
    const top = max ?? Math.max(...values, 1);
    const pts = values.map((v, i) => [(i / (values.length - 1)) * w, h - 2 - (Math.min(v, top) / top) * (h - 4)]);
    const line = pts.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(1)},${p[1].toFixed(1)}`).join('');
    return `<svg class="spark" viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" aria-hidden="true">
      <path d="${line}L${w},${h}L0,${h}Z" fill="${color}" opacity=".12"/>
      <path d="${line}" fill="none" stroke="${color}" stroke-width="2" vector-effect="non-scaling-stroke" stroke-linejoin="round"/></svg>`;
  }

  // Hourly bars, oldest first; each bar has a taller invisible hit target.
  function hourBars(values, color, { w = 120, h = 24, label = 'requests' } = {}) {
    const n = values.length || 24;
    const gap = 2;
    const bw = Math.max(1, (w - gap * (n - 1)) / n);
    const top = Math.max(...values, 1);
    const now = new Date();
    let out = `<svg class="bars" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" role="img" aria-label="${esc(label)} per hour, last 24 hours">`;
    values.forEach((v, i) => {
      const x = i * (bw + gap);
      const bh = v > 0 ? Math.max(2, (v / top) * (h - 1)) : 0;
      const hoursAgo = n - 1 - i;
      const start = new Date(now.getTime() - hoursAgo * 3600e3);
      const tip = `${hoursAgo === 0 ? 'this hour' : `${hoursAgo}h ago`} (${String(start.getUTCHours()).padStart(2, '0')}:00 UTC)\n${fmtNum(v)} ${label}`;
      out += `<rect x="${x.toFixed(1)}" y="0" width="${(bw + gap).toFixed(1)}" height="${h}" fill="transparent" data-tip="${esc(tip)}"/>`;
      if (bh > 0) out += `<rect x="${x.toFixed(1)}" y="${(h - bh).toFixed(1)}" width="${bw.toFixed(1)}" height="${bh.toFixed(1)}" rx="1.5" fill="${color}" pointer-events="none"/>`;
      else out += `<rect x="${x.toFixed(1)}" y="${h - 1}" width="${bw.toFixed(1)}" height="1" fill="var(--grid)" pointer-events="none"/>`;
    });
    return out + '</svg>';
  }

  function statusMix(st) {
    const total = st.reduce((a, b) => a + b, 0);
    if (!total) return '<span class="muted">—</span>';
    const names = ['2xx', '3xx', '4xx', '5xx'];
    const segs = st.map((v, i) => (v ? `<span style="flex:${v};background:var(--st-${names[i]})" data-tip="${esc(`${names[i]}: ${fmtNum(v)} (${((100 * v) / total).toFixed(1)}%)`)}"></span>` : '')).join('');
    return `<div class="status-mix" role="img" aria-label="${esc(names.map((n, i) => `${n} ${st[i]}`).join(', '))}">${segs}</div>`;
  }

  // Full line chart with axis, gridlines, crosshair and tooltip.
  function mountLineChart(el, samples, key, color, label) {
    const w = Math.max(280, el.clientWidth);
    const h = 180;
    const m = { l: 34, r: 8, t: 8, b: 22 };
    const iw = w - m.l - m.r, ih = h - m.t - m.b;
    if (!samples || samples.length < 2) {
      el.innerHTML = `<div class="empty">Not enough data yet. The sampler fills this in over the next minutes.</div>`;
      return;
    }
    const t0 = samples[0].t, t1 = samples[samples.length - 1].t;
    // A mostly idle box would be a flat line on 0-100%, so round the top up instead.
    const peak = Math.max(...samples.map((s) => s[key]));
    const top = [10, 20, 25, 50, 100].find((c) => peak * 1.1 <= c) || 100;
    const x = (t) => m.l + ((t - t0) / Math.max(1, t1 - t0)) * iw;
    const y = (v) => m.t + ih - (Math.min(top, Math.max(0, v)) / top) * ih;
    let svg = `<svg viewBox="0 0 ${w} ${h}" width="${w}" height="${h}" role="img" aria-label="${esc(label)} over time">`;
    for (let i = 0; i <= 4; i++) {
      const g = (top * i) / 4;
      svg += `<line class="${g ? 'gridline' : 'baseline'}" x1="${m.l}" x2="${w - m.r}" y1="${y(g)}" y2="${y(g)}"/>`;
      svg += `<text class="tick" x="${m.l - 6}" y="${y(g) + 3.5}" text-anchor="end">${Number.isInteger(g) ? g : g.toFixed(1)}%</text>`;
    }
    const span = t1 - t0;
    const ticks = 5;
    for (let i = 0; i <= ticks; i++) {
      const t = t0 + (span * i) / ticks;
      const d = new Date(t * 1000);
      const txt = span > 2 * 86400
        ? d.toLocaleDateString('en-GB', { weekday: 'short', day: 'numeric', timeZone: 'UTC' })
        : `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`;
      const anchor = i === 0 ? 'start' : i === ticks ? 'end' : 'middle';
      svg += `<text class="tick" x="${x(t)}" y="${h - 5}" text-anchor="${anchor}">${txt}</text>`;
    }
    // Break the line where samples are missing (restarts, sysstat gaps).
    const maxGap = Math.max(3 * ((t1 - t0) / samples.length), 1300);
    let d = '';
    let area = '';
    let seg = [];
    const flush = () => {
      if (seg.length > 1) {
        const p = seg.map((s, i) => `${i ? 'L' : 'M'}${x(s.t).toFixed(1)},${y(s[key]).toFixed(1)}`).join('');
        d += p;
        area += `${p}L${x(seg[seg.length - 1].t).toFixed(1)},${y(0)}L${x(seg[0].t).toFixed(1)},${y(0)}Z`;
      }
      seg = [];
    };
    samples.forEach((s, i) => {
      if (i && s.t - samples[i - 1].t > maxGap) flush();
      seg.push(s);
    });
    flush();
    svg += `<path d="${area}" fill="${color}" opacity=".1"/>`;
    svg += `<path d="${d}" fill="none" stroke="${color}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`;
    svg += `<line class="cross" x1="0" x2="0" y1="${m.t}" y2="${m.t + ih}" visibility="hidden"/>`;
    svg += `<circle class="hover-dot" r="4.5" fill="${color}" visibility="hidden"/>`;
    svg += `<rect class="hit" x="${m.l}" y="${m.t}" width="${iw}" height="${ih}" fill="transparent"/>`;
    svg += '</svg>';
    el.innerHTML = svg;

    const root = el.querySelector('svg');
    const cross = root.querySelector('.cross');
    const dot = root.querySelector('.hover-dot');
    const hit = root.querySelector('.hit');
    const hide = () => { cross.setAttribute('visibility', 'hidden'); dot.setAttribute('visibility', 'hidden'); hideTip(); };
    hit.addEventListener('pointerleave', hide);
    hit.addEventListener('pointermove', (ev) => {
      const r = root.getBoundingClientRect();
      const px = ((ev.clientX - r.left) / r.width) * w;
      const t = t0 + ((px - m.l) / iw) * (t1 - t0);
      let lo = 0, hi = samples.length - 1;
      while (hi - lo > 1) { const mid = (lo + hi) >> 1; if (samples[mid].t < t) lo = mid; else hi = mid; }
      const s = Math.abs(samples[lo].t - t) < Math.abs(samples[hi].t - t) ? samples[lo] : samples[hi];
      const sx = x(s.t), sy = y(s[key]);
      cross.setAttribute('x1', sx); cross.setAttribute('x2', sx); cross.setAttribute('visibility', 'visible');
      dot.setAttribute('cx', sx); dot.setAttribute('cy', sy); dot.setAttribute('visibility', 'visible');
      const when = new Date(s.t * 1000).toLocaleString('en-GB', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone: 'UTC' });
      showTip(`<div class="tt-time">${esc(when)} UTC</div><div class="tt-row"><span><span class="swatch" style="background:${color}"></span> ${esc(label)}</span><b>${s[key].toFixed(1)}%</b></div>`,
        r.left + (sx / w) * r.width, r.top + (sy / h) * r.height, true);
    });
  }

  // ---------- tooltip ----------
  function showTip(html, cx, cy, isHtml) {
    if (isHtml) tooltip.innerHTML = html;
    else tooltip.innerHTML = html.split('\n').map((l, i) => (i ? `<div>${esc(l)}</div>` : `<div class="tt-time">${esc(l)}</div>`)).join('');
    tooltip.hidden = false;
    const tw = tooltip.offsetWidth, th = tooltip.offsetHeight;
    let left = cx + 14, top = cy - th - 10;
    if (left + tw > window.innerWidth - 8) left = cx - tw - 14;
    if (top < 8) top = cy + 14;
    tooltip.style.left = `${Math.max(8, left)}px`;
    tooltip.style.top = `${top}px`;
  }
  function hideTip() { tooltip.hidden = true; }
  document.addEventListener('pointerover', (ev) => {
    const el = ev.target.closest && ev.target.closest('[data-tip]');
    if (!el) return;
    const r = el.getBoundingClientRect();
    showTip(el.getAttribute('data-tip'), r.left + r.width / 2, r.top, false);
  });
  document.addEventListener('pointerout', (ev) => {
    if (ev.target.closest && ev.target.closest('[data-tip]')) hideTip();
  });

  // ---------- data ----------
  let S = null;
  let lastOK = 0;
  let lastErr = '';
  const hist = { range: '1h', data: null, at: 0 };
  const spark = { data: null, at: 0 };
  const journals = {};
  let showAllAlerts = false;

  async function getJSON(url) {
    const r = await fetch(url, { cache: 'no-store' });
    if (!r.ok) throw new Error(`${url}: ${r.status}`);
    return r.json();
  }

  async function refresh() {
    try {
      S = await getJSON('/api/state');
      lastOK = Date.now();
      lastErr = '';
    } catch (e) {
      lastErr = String(e.message || e);
    }
    const r = route();
    const jobs = [];
    if (Date.now() - spark.at > 30000) {
      jobs.push(getJSON('/api/history?range=1h').then((d) => { spark.data = d; spark.at = Date.now(); }).catch(() => {}));
    }
    if (r.tab === 'host' && (hist.data?.range !== hist.range || Date.now() - hist.at > 60000)) {
      jobs.push(loadHistory());
    }
    if (r.tab === 'project' && r.id) {
      jobs.push(getJSON(`/api/journal?project=${encodeURIComponent(r.id)}`).then((j) => { journals[r.id] = j; }).catch(() => {}));
    }
    await Promise.all(jobs);
    render();
  }

  async function loadHistory() {
    try {
      hist.data = await getJSON(`/api/history?range=${hist.range}`);
      hist.at = Date.now();
    } catch (e) {
      hist.data = { range: hist.range, samples: [], error: String(e.message || e) };
    }
  }

  // ---------- routing ----------
  function route() {
    const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map((p) => { try { return decodeURIComponent(p); } catch (e) { return p; } });
    if (!parts.length) return { tab: 'overview' };
    if (parts[0] === 'project') return { tab: 'project', id: parts[1] };
    return { tab: parts[0] };
  }
  window.addEventListener('hashchange', () => { window.scrollTo(0, 0); refresh(); });
  document.addEventListener('visibilitychange', () => { if (!document.hidden) refresh(); });

  // ---------- chrome ----------
  function renderChrome() {
    const r = route();
    document.querySelectorAll('#tabs a').forEach((a) => {
      const t = a.dataset.tab;
      const on = t === r.tab || (t === 'projects' && r.tab === 'project');
      if (on) a.setAttribute('aria-current', 'page'); else a.removeAttribute('aria-current');
    });
    const live = document.getElementById('live');
    const liveText = document.getElementById('live-text');
    live.classList.toggle('down', !!lastErr);
    live.classList.toggle('stale', !lastErr && S?.probe?.stale);
    liveText.textContent = lastErr ? 'dashboard unreachable' : lastOK ? `updated ${fmtDur((Date.now() - lastOK) / 1000)} ago` : 'loading';
    if (!S) return;
    const h = S.host;
    document.getElementById('host-line').textContent = `${h.hostname || 'host'} · ${h.cpus || '?'} vCPU · ${fmtBytes(h.mem_total)} RAM · up ${fmtDur(h.uptime_seconds)}`;
    document.getElementById('demo-banner').hidden = !S.demo;
    const p = S.probe;
    const probeText = p.stale ? `probe snapshot missing or stale${p.error ? `: ${p.error}` : ''}` : `probe snapshot ${fmtDur(p.age_seconds)} old`;
    document.getElementById('footer').innerHTML = `<span>Read-only. Env files show names, never values.</span><span>${esc(probeText)}</span><span>v${esc(S.version)}</span>`;
    document.title = `${countAlerts('critical') ? `(${countAlerts('critical')}) ` : ''}VPS dashboard`;
  }
  const countAlerts = (lvl) => (S?.alerts || []).filter((a) => a.level === lvl).length;

  // ---------- views ----------
  function render() {
    hideTip();
    renderChrome();
    if (!S) {
      view.innerHTML = `<div class="card empty">${lastErr ? `Can't reach the dashboard API (${esc(lastErr)}). Is the SSH tunnel up?` : 'Loading…'}</div>`;
      return;
    }
    const r = route();
    const views = { overview, projects, project: () => project(r.id), host, envs, network };
    view.innerHTML = (views[r.tab] || overview)();
    afterRender(r);
  }

  function afterRender(r) {
    const more = document.getElementById('more-alerts');
    if (more) more.addEventListener('click', () => { showAllAlerts = !showAllAlerts; render(); });
    if (r.tab === 'host') {
      document.querySelectorAll('[data-range]').forEach((b) => b.addEventListener('click', async () => {
        hist.range = b.dataset.range;
        render();
        await loadHistory();
        render();
      }));
      const samples = hist.data?.range === hist.range ? hist.data.samples : null;
      const charts = [['cpu', 'cpu', 'var(--s-cpu)', 'CPU'], ['mem', 'mem', 'var(--s-mem)', 'Memory used'], ['swap', 'swap', 'var(--s-swap)', 'Swap used']];
      for (const [id, key, color, label] of charts) {
        const el = document.getElementById(`chart-${id}`);
        if (el) mountLineChart(el, samples, key, color, label);
      }
    }
    document.querySelectorAll('tr[data-href]').forEach((tr) => tr.addEventListener('click', () => { location.hash = tr.dataset.href; }));
  }

  function alertsSection() {
    const alerts = S.alerts || [];
    const crit = countAlerts('critical'), warn = countAlerts('warning');
    const sub = alerts.length ? `${crit} critical · ${warn} warning · ${alerts.length - crit - warn} info` : '';
    let body;
    if (!alerts.length) {
      body = `<div class="card alerts-ok">${badge('good', 'All clear')}<span class="ink2">Every unit is up, every check passes, backups are fresh and every port is where it should be.</span></div>`;
    } else {
      const shown = showAllAlerts ? alerts : alerts.slice(0, 6);
      const lvlLabel = { critical: 'Critical', warning: 'Warning', info: 'Info' };
      body = `<div class="alerts">${shown.map((a) => `<div class="alert">${badge(a.level, lvlLabel[a.level] || a.level)}<span class="where">${esc(projectName(a.project) || 'host')} · ${esc(a.area)}</span><span class="msg">${esc(a.message)}</span></div>`).join('')}
        ${alerts.length > 6 ? `<button class="more-btn" id="more-alerts" type="button">${showAllAlerts ? 'Show fewer' : `Show all ${alerts.length}`}</button>` : ''}</div>`;
    }
    return `<section class="section"><div class="section-head"><h2>Alerts</h2><span class="sub">${sub}</span></div>${body}</section>`;
  }
  const projectName = (id) => (S.projects || []).find((p) => p.id === id)?.name || id;

  function hostTiles() {
    const h = S.host;
    const samples = spark.data?.samples || [];
    const memUsed = h.mem_total ? 100 * (1 - h.mem_available / h.mem_total) : 0;
    const swapUsed = h.swap_total ? 100 * (1 - h.swap_free / h.swap_total) : 0;
    const disk = (h.disks || [])[0];
    const diskUsed = disk && disk.total ? 100 * (1 - disk.free / disk.total) : 0;
    const tile = (label, color, value, unit, sub, chart) => `<div class="card tile">
      <div class="tile-label">${color ? `<span class="swatch" style="background:${color}"></span>` : ''}${esc(label)}</div>
      <div class="tile-value">${value}<span class="unit">${unit}</span></div>
      <div class="tile-sub">${sub}</div>${chart}</div>`;
    return `<div class="grid tiles">
      ${tile('CPU', 'var(--s-cpu)', (h.cpu_percent ?? 0).toFixed(1), '%', `load ${h.load.map((l) => l.toFixed(2)).join(' · ')} · steal ${fmtPct(h.steal_percent)}`, sparkline(samples.map((s) => s.cpu), 'var(--s-cpu)', { max: Math.max(10, ...samples.map((s) => s.cpu)) }))}
      ${tile('Memory', 'var(--s-mem)', memUsed.toFixed(0), '%', `${fmtBytes(h.mem_total - h.mem_available)} used · ${fmtBytes(h.mem_available)} available`, sparkline(samples.map((s) => s.mem), 'var(--s-mem)', { max: 100 }))}
      ${tile('Swap', 'var(--s-swap)', swapUsed.toFixed(swapUsed < 1 ? 1 : 0), '%', `${fmtBytes(h.swap_total - h.swap_free)} of ${fmtBytes(h.swap_total)}`, sparkline(samples.map((s) => s.swap), 'var(--s-swap)', { max: 100 }))}
      ${tile('Disk', '', diskUsed.toFixed(0), '%', disk ? `${fmtBytes(disk.free)} free of ${fmtBytes(disk.total)} on ${esc(disk.path)}` : 'no disk data', `<div style="margin-top:14px">${meter(diskUsed)}</div>`)}
    </div>`;
  }

  function projectCard(p) {
    const units = p.units || [];
    const up = units.filter((u) => u.active === 'active').length;
    const checks = p.checks || [];
    const passing = checks.filter((c) => c.ok).length;
    const checked = checks.filter((c) => !zeroTime(c.checked_at)).length;
    const avgLat = checked ? checks.reduce((a, c) => a + (c.latency_ms || 0), 0) / checked : 0;
    const ports = p.ports || [];
    const exposed = ports.filter((x) => x.status === 'exposed').length;
    const listening = ports.filter((x) => x.listening && x.listening.length).length;
    const d = p.deploy || {};
    const backups = p.backups || [];
    const worstBackup = backups.find((b) => b.status !== 'ok') || backups[0];
    const j = p.journal || {};
    const rows = [];
    if (checks.length) rows.push(['Health', `${checked ? badge(passing === checks.length ? 'good' : 'critical', `${passing}/${checks.length} passing`) : plain('checking…')}${checked ? `<span class="muted small">${avgLat.toFixed(0)} ms avg</span>` : ''}`]);
    if (d.method && d.method !== 'none') rows.push(['Deploy', `${deployBadge(d)}<span class="sha">${short(d.commit)}</span><span class="muted small t">${d.deployed_at && !zeroTime(d.deployed_at) ? `deployed ${ago(d.deployed_at)}` : ''}</span>`]);
    if (backups.length) rows.push(['Backup', `${backupBadge(worstBackup)}<span class="muted small t">${worstBackup.newest ? `newest ${ago(worstBackup.newest_time)}` : esc(worstBackup.error || '')}</span>`]);
    if (ports.length) rows.push(['Ports', exposed ? badge('critical', `${exposed} exposed`) : `<span class="t">${ports.map((x) => x.port).join(', ')} <span class="muted small">${listening === ports.length ? 'all bound as expected' : `${ports.length - listening} not listening`}</span></span>`]);
    rows.push(['Logs 24h', `<span class="t">${fmtNum(j.errors_24h)} errors · ${fmtNum(j.warnings_24h)} warnings</span>${j.per_hour ? hourBars(j.per_hour, 'var(--s-vol)', { w: 96, h: 18, label: 'error lines' }) : ''}`]);
    return `<a class="card pcard" href="#/project/${encodeURIComponent(p.id)}">
      <div class="pcard-head"><div><div class="pcard-name">${esc(p.name)}</div><div class="pcard-desc">${esc(p.description)}</div></div>${projectBadge(p.status)}</div>
      <div class="kv">
        <div><div class="k">CPU</div><div class="v">${fmtPct(p.cpu_percent)}</div></div>
        <div><div class="k">Memory</div><div class="v">${fmtBytes(p.memory_bytes)}</div></div>
        <div><div class="k">Units</div><div class="v">${up}/${units.length} up</div></div>
      </div>
      <div class="pcard-rows">${rows.map(([k, v]) => `<div class="prow"><span class="k">${k}</span><span class="v">${v}</span></div>`).join('')}</div>
    </a>`;
  }

  function shareCard(title, sub, key, fmt, color) {
    const ps = [...S.projects].sort((a, b) => b[key] - a[key]);
    const top = Math.max(...ps.map((p) => p[key]), key === 'cpu_percent' ? 5 : 1);
    return `<div class="card"><div class="card-head"><h2>${title}</h2><span class="muted small">${sub}</span></div><div class="share">
      ${ps.map((p) => `<div class="share-row"><span class="name">${esc(p.name)}</span><div class="share-track"><span style="width:${((100 * p[key]) / top).toFixed(1)}%;background:${color}"></span></div><span class="val">${fmt(p[key])}</span></div>`).join('')}
    </div></div>`;
  }

  function overview() {
    const h = S.host;
    const projMem = S.projects.reduce((a, p) => a + p.memory_bytes, 0);
    return `${alertsSection()}
      <section class="section"><div class="section-head"><h2>Host now</h2><span class="sub">last hour in the sparklines · <a href="#/host">history</a></span></div>${hostTiles()}</section>
      <section class="section"><div class="section-head"><h2>Projects</h2><span class="sub">${S.projects.length} projects · click one for units, logs and deploys</span></div>
        <div class="grid cards">${S.projects.map(projectCard).join('')}</div></section>
      <section class="section grid two">
        ${shareCard('Memory by project', `${fmtBytes(projMem)} of ${fmtBytes(h.mem_total - h.mem_available)} in use`, 'memory_bytes', fmtBytes, 'var(--s-mem)')}
        ${shareCard('CPU by project', 'share of one vCPU, last 10 s', 'cpu_percent', (v) => fmtPct(v), 'var(--s-cpu)')}
      </section>`;
  }

  function projects() {
    return `<section class="section"><div class="section-head"><h2>Projects</h2><span class="sub">every project on one screen</span></div>
      <div class="card flush"><div class="table-wrap"><table>
        <thead><tr><th>Project</th><th>Status</th><th class="num">Units</th><th class="num">CPU</th><th class="num">Memory</th><th>Health</th><th>Deploy</th><th>Backup</th><th class="num">Errors 24h</th><th>Env files</th></tr></thead>
        <tbody>${S.projects.map((p) => {
          const up = p.units.filter((u) => u.active === 'active').length;
          const checks = p.checks || [];
          const passing = checks.filter((c) => c.ok).length;
          const b = (p.backups || []).find((x) => x.status !== 'ok') || (p.backups || [])[0];
          return `<tr data-href="#/project/${encodeURIComponent(p.id)}" style="cursor:pointer">
            <td><a href="#/project/${encodeURIComponent(p.id)}"><b>${esc(p.name)}</b></a><div class="sub">${esc(p.description)}</div></td>
            <td>${projectBadge(p.status)}</td>
            <td class="num">${up}/${p.units.length}</td>
            <td class="num">${fmtPct(p.cpu_percent)}</td>
            <td class="num">${fmtBytes(p.memory_bytes)}</td>
            <td>${checks.length ? badge(passing === checks.length ? 'good' : 'critical', `${passing}/${checks.length}`) : '<span class="muted">—</span>'}</td>
            <td><div style="display:flex;gap:6px;align-items:center">${deployBadge(p.deploy)}<span class="sha">${short(p.deploy?.commit)}</span></div></td>
            <td>${b ? `${backupBadge(b)} <span class="sub">${b.newest ? ago(b.newest_time) : ''}</span>` : '<span class="muted">—</span>'}</td>
            <td class="num">${fmtNum(p.journal?.errors_24h)}</td>
            <td>${(p.env_files || []).length ? `<a href="#/envs">${p.env_files.length}</a>` : '<span class="muted">none</span>'}</td>
          </tr>`;
        }).join('')}</tbody></table></div></div></section>`;
  }

  function project(id) {
    const p = (S.projects || []).find((x) => x.id === id);
    if (!p) return `<div class="card empty">No project called “${esc(id)}”. <a href="#/projects">All projects</a></div>`;
    const units = p.units || [];
    const up = units.filter((u) => u.active === 'active').length;
    const checks = p.checks || [];
    const passing = checks.filter((c) => c.ok).length;
    const d = p.deploy || {};
    const j = journals[p.id] || p.journal || {};
    const alerts = (S.alerts || []).filter((a) => a.project === p.id);
    const repo = p.repo ? `<a href="https://github.com/${esc(p.repo)}" target="_blank" rel="noopener noreferrer">${esc(p.repo)}</a>` : '';

    const unitRows = units.map((u) => {
      const memPct = u.memory_max ? (100 * u.memory_bytes) / u.memory_max : 0;
      const isTimer = u.name.endsWith('.timer');
      return `<tr>
        <td><b>${esc(u.name)}</b><div class="sub">${esc(u.description)}</div></td>
        <td>${unitBadge(u)}</td>
        <td class="nowrap">${isTimer ? `last ${ago(u.last_trigger)}<div class="sub">next ${until(u.next_elapse)}</div>` : `${ago(u.active_since)}<div class="sub">${fmtDate(u.active_since)}</div>`}</td>
        <td class="num">${u.restarts ? `<span class="badge warning">${ICONS.warning}${u.restarts}</span>` : '0'}</td>
        <td class="num">${isTimer ? '—' : `${fmtBytes(u.memory_bytes)}${u.memory_max ? `<div class="sub">of ${fmtBytes(u.memory_max)}</div>${meter(memPct, 'thin')}` : '<div class="sub">no limit</div>'}`}</td>
        <td class="num">${isTimer ? '—' : fmtPct(u.cpu_percent)}</td>
        <td class="num">${u.tasks || '—'}</td></tr>`;
    }).join('');

    const deployCard = (!d.method || d.method === 'none') ? '' : `<div class="card"><div class="card-head"><h2>Deploy</h2>${deployBadge(d)}</div>
      <div class="deploy-grid">
        <div class="deploy-side"><div class="k">On the server${d.exact ? '' : ' (closest match)'}</div>
          <div><span class="sha">${short(d.commit)}</span>${d.dirty ? ' <span class="chip">uncommitted changes</span>' : ''}</div>
          <div class="subject">${esc(d.subject || '—')}</div>
          <div class="muted small">committed ${ago(d.commit_time)} · files changed ${ago(d.deployed_at)}${d.needs_restart ? ' · <b>restart pending?</b>' : ''}</div></div>
        <div class="deploy-side"><div class="k">${d.main_source === 'local-ref' ? 'origin/main when copied' : 'GitHub main'}</div>
          <div><span class="sha">${short(d.main_commit)}</span></div>
          <div class="subject">${esc(d.main_subject || '—')}</div>
          <div class="muted small">${d.main_time && !zeroTime(d.main_time) ? `committed ${ago(d.main_time)}` : ''}${d.behind > 0 ? ` · ${d.behind} commit${d.behind === 1 ? '' : 's'} ahead of the server` : ''}</div></div>
      </div>
      <div class="muted small" style="margin-top:10px">How this is known: ${esc({ go_binary: 'the commit Go stamps into the binary', git_dir: 'the .git folder copied with the code', tree: `hashing ${fmtNum(d.compared_files)} tracked files on disk and matching them to recent commits` }[d.method] || d.method)}.${d.note ? ` ${esc(d.note)}.` : ''}${d.error ? ` <span class="ink2">${esc(d.error)}</span>` : ''}${(d.differing_paths || []).length ? `<div style="margin-top:6px">Files that differ: ${d.differing_paths.map((x) => `<code>${esc(x)}</code>`).join(', ')}${d.differing_files > d.differing_paths.length ? ` and ${d.differing_files - d.differing_paths.length} more` : ''}</div>` : ''}</div>
    </div>`;

    const checkRows = checks.map((c) => `<tr><td class="mono" style="overflow-wrap:anywhere">${esc(c.url)}</td>
      <td>${zeroTime(c.checked_at) ? plain('pending') : c.ok ? badge('good', String(c.status)) : badge('critical', c.status ? String(c.status) : 'error')}${c.error ? `<div class="sub">${esc(c.error)}</div>` : ''}</td>
      <td class="num">${c.latency_ms ? `${c.latency_ms.toFixed(0)} ms` : '—'}</td><td class="num muted">${ago(c.checked_at)}</td></tr>`).join('');
    const portRows = (p.ports || []).map((x) => `<tr><td class="num"><b>${x.port}</b></td><td>${esc(x.label)}</td><td class="mono">${esc((x.listening || []).join(', ') || '—')}</td><td>${portBadge(x)}</td></tr>`).join('');
    const backupRows = (p.backups || []).map((b) => `<tr><td><b>${esc(b.label)}</b><div class="sub mono">${esc(b.dir)}/${esc(b.glob)}</div></td><td>${backupBadge(b)}</td>
      <td class="nowrap">${b.newest ? `${ago(b.newest_time)}<div class="sub">${fmtDate(b.newest_time)}</div>` : `<span class="sub">${esc(b.error || 'none')}</span>`}</td>
      <td class="num">${fmtBytes(b.newest_bytes)}<div class="sub">${b.count} kept · ${fmtBytes(b.total_bytes)}</div></td></tr>`).join('');
    const envs = (S.env_files || []).filter((e) => e.project === p.id);
    const envRows = envs.map((e) => `<tr><td class="mono" style="overflow-wrap:anywhere">${esc(e.path)}</td><td>${e.secret_file ? plain('single secret') : `${(e.vars || []).length} names`}</td><td class="mono">${esc(e.mode || '—')}</td><td class="muted nowrap">${ago(e.mod_time)}</td></tr>`).join('');
    const statics = (p.static_sites || []).map((s) => `<tr><td><b>${esc(s.label)}</b><div class="sub mono">${esc(s.path)}</div></td><td class="nowrap">${s.error ? esc(s.error) : `${ago(s.deployed_at)}<div class="sub">${fmtDate(s.deployed_at)}</div>`}</td></tr>`).join('');

    const lines = j.lines || [];
    const lvl = { error: 'critical', warning: 'warning' };
    const logBody = lines.length
      ? `<div class="loglist">${lines.map((l) => `<div class="logline"><div class="meta">${badge(lvl[l.level] || 'info', l.level)}<span>${ago(l.time)}</span>${l.count > 1 ? `<span class="chip">×${l.count}</span>` : ''}</div>
          <div><div class="unit">${esc(l.unit)}</div><div class="text">${esc(l.message)}</div></div></div>`).join('')}</div>`
      : `<div class="empty">No error or warning lines in the last 24 hours${S.probe.stale ? ' (or the probe is not running)' : ''}.</div>`;

    const tableCard = (title, head, rows, extra = '') => rows ? `<div class="card flush"><div class="card-head"><h2>${title}</h2>${extra}</div><div class="table-wrap"><table><thead><tr>${head}</tr></thead><tbody>${rows}</tbody></table></div></div>` : '';

    return `<div class="crumbs"><a href="#/projects">Projects</a> / ${esc(p.name)}</div>
      <div class="phead"><div><h1>${esc(p.name)}</h1><div class="phead-meta">${esc(p.description)}${repo ? ` · ${repo}` : ''}</div></div>${projectBadge(p.status)}</div>
      <div class="stats-strip">
        <div class="stat"><div class="k">CPU</div><div class="v">${fmtPct(p.cpu_percent)}</div></div>
        <div class="stat"><div class="k">Memory</div><div class="v">${fmtBytes(p.memory_bytes)}</div></div>
        <div class="stat"><div class="k">Units up</div><div class="v">${up}/${units.length}</div></div>
        <div class="stat"><div class="k">Health</div><div class="v">${checks.length ? `${passing}/${checks.length}` : '—'}</div></div>
        <div class="stat"><div class="k">Errors 24h</div><div class="v">${fmtNum(j.errors_24h)}</div></div>
        <div class="stat"><div class="k">Deployed</div><div class="v">${d.deployed_at && !zeroTime(d.deployed_at) ? ago(d.deployed_at) : '—'}</div></div>
      </div>
      ${alerts.length ? `<section class="section"><div class="alerts">${alerts.map((a) => `<div class="alert">${badge(a.level, a.level[0].toUpperCase() + a.level.slice(1))}<span class="where">${esc(a.area)}</span><span class="msg">${esc(a.message)}</span></div>`).join('')}</div></section>` : ''}
      ${deployCard ? `<section class="section">${deployCard}</section>` : ''}
      <section class="section">${tableCard('Units', '<th>Unit</th><th>State</th><th>Since</th><th class="num">Restarts</th><th class="num">Memory</th><th class="num">CPU</th><th class="num">Tasks</th>', unitRows)}</section>
      <section class="section grid two">
        ${tableCard('Health checks', '<th>URL</th><th>Result</th><th class="num">Latency</th><th class="num">Checked</th>', checkRows) || '<div class="card empty">No public URLs to check.</div>'}
        ${tableCard('Ports', '<th class="num">Port</th><th>Purpose</th><th>Bound to</th><th>Status</th>', portRows) || '<div class="card empty">No reserved ports.</div>'}
      </section>
      <section class="section grid two">
        ${tableCard('Backups', '<th>Job</th><th>Status</th><th>Newest</th><th class="num">Size</th>', backupRows) || '<div class="card empty">No backups configured for this project.</div>'}
        ${tableCard('Env files', '<th>File</th><th>Contents</th><th>Mode</th><th>Changed</th>', envRows, '<a class="small" href="#/envs">variable names →</a>') || '<div class="card empty">No env files.</div>'}
      </section>
      ${statics ? `<section class="section">${tableCard('Static sites', '<th>Site</th><th>Last deployed</th>', statics)}</section>` : ''}
      <section class="section"><div class="card flush"><div class="card-head"><div><h2>Logs</h2><div class="muted small">errors and warnings from the journal, last 24 h, secrets redacted</div></div>
        <div style="text-align:right">${hourBars(j.per_hour || new Array(24).fill(0), 'var(--s-vol)', { w: 168, h: 28, label: 'error lines' })}<div class="muted small">${fmtNum(j.errors_24h)} errors · ${fmtNum(j.warnings_24h)} warnings</div></div></div>
        ${logBody}</div></section>`;
  }

  function host() {
    const h = S.host;
    const ranges = [['1h', 'Last hour'], ['6h', '6 hours'], ['24h', '24 hours'], ['7d', '7 days']];
    const data = hist.data?.range === hist.range ? hist.data : null;
    let src = 'loading…';
    if (data?.source === 'sysstat') src = 'sysstat, 10-minute averages';
    else if (data?.source) src = `the dashboard's own sampler, ${data.step_seconds}-second points`;
    else if (data) src = 'unavailable';
    const now = (key) => {
      const s = data?.samples || [];
      return s.length ? `${s[s.length - 1][key].toFixed(1)}%` : '—';
    };
    const chartCard = (id, label, color, key) => `<div class="card"><div class="chart-title"><span class="label"><span class="swatch" style="background:${color}"></span>${label}</span><span class="now">${now(key)}</span></div><div class="chart" id="chart-${id}"></div></div>`;
    const allUnits = S.projects.flatMap((p) => p.units.filter((u) => !u.name.endsWith('.timer')).map((u) => ({ ...u, project: p }))).sort((a, b) => b.memory_bytes - a.memory_bytes);
    return `<section class="section"><div class="section-head"><h2>Host</h2>
        <div class="seg" role="group" aria-label="Time range">${ranges.map(([r, l]) => `<button type="button" data-range="${r}" aria-pressed="${r === hist.range}">${l}</button>`).join('')}</div></div>
        ${hostTiles()}</section>
      <section class="section">
        ${chartCard('cpu', 'CPU busy', 'var(--s-cpu)', 'cpu')}
        <div class="grid two" style="margin-top:16px">${chartCard('mem', 'Memory used', 'var(--s-mem)', 'mem')}${chartCard('swap', 'Swap used', 'var(--s-swap)', 'swap')}</div>
        <div class="muted small" style="margin-top:8px">Source: ${esc(src)}. Memory counts reclaimable cache as free, like the "available" figure.${data?.error ? ` <span class="ink2">${esc(data.error)}</span>` : ''}</div>
      </section>
      <section class="section"><div class="card flush"><div class="card-head"><h2>Biggest units</h2><span class="muted small">memory includes the unit's page cache, as systemd counts it</span></div><div class="table-wrap"><table>
        <thead><tr><th>Unit</th><th>Project</th><th class="num">Memory</th><th class="num">Peak</th><th class="num">CPU</th><th class="num">Restarts</th><th>Since</th></tr></thead>
        <tbody>${allUnits.map((u) => `<tr data-href="#/project/${encodeURIComponent(u.project.id)}" style="cursor:pointer"><td><b>${esc(u.name)}</b></td><td>${esc(u.project.name)}</td>
          <td class="num">${fmtBytes(u.memory_bytes)}${u.memory_max ? `<div class="sub">${((100 * u.memory_bytes) / u.memory_max).toFixed(0)}% of limit</div>` : ''}</td><td class="num">${fmtBytes(u.memory_peak)}</td>
          <td class="num">${fmtPct(u.cpu_percent)}</td><td class="num">${u.restarts}</td><td class="nowrap muted">${ago(u.active_since)}</td></tr>`).join('')}</tbody></table></div></div></section>
      <section class="section"><div class="card flush"><div class="card-head"><h2>Machine</h2></div><div class="table-wrap"><table><tbody>
        <tr><td class="muted">Hostname</td><td class="mono">${esc(h.hostname)}</td></tr>
        <tr><td class="muted">Kernel</td><td class="mono">${esc(h.kernel)}</td></tr>
        <tr><td class="muted">CPUs</td><td>${h.cpus}</td></tr>
        <tr><td class="muted">Uptime</td><td>${fmtDur(h.uptime_seconds)}</td></tr>
        <tr><td class="muted">Load average</td><td>${h.load.map((l) => l.toFixed(2)).join(' · ')} <span class="muted small">(1, 5, 15 min)</span></td></tr>
        <tr><td class="muted">CPU steal / iowait</td><td>${fmtPct(h.steal_percent)} / ${fmtPct(h.iowait_percent)}</td></tr>
        ${(h.disks || []).map((dk) => `<tr><td class="muted">Disk ${esc(dk.path)}</td><td><div style="max-width:320px">${fmtBytes(dk.total - dk.free)} used of ${fmtBytes(dk.total)}${meter(100 * (1 - dk.free / dk.total))}</div></td></tr>`).join('')}
      </tbody></table></div></div></section>`;
  }

  function envs() {
    const files = S.env_files || [];
    const copies = S.env_copies || [];
    const order = S.projects.map((p) => p.id);
    files.sort((a, b) => order.indexOf(a.project) - order.indexOf(b.project));
    const card = (e) => {
      const vars = e.vars || [];
      const empty = vars.filter((v) => !v.set).length;
      const modeOK = !e.expect_mode || e.mode === e.expect_mode;
      const ownerOK = !e.expect_owner || e.owner === e.expect_owner;
      let state;
      if (e.error) state = badge('warning', 'unreadable');
      else if (!e.exists) state = badge('critical', 'missing');
      else if (!modeOK || !ownerOK) state = badge('warning', !modeOK ? `mode ${e.mode}` : `owner ${e.owner}`);
      else state = badge('good', 'permissions ok');
      const body = e.secret_file
        ? `<div class="vars"><span class="var ${e.size > 0 ? '' : 'empty'}"><span class="dot"></span>single secret · ${e.size > 0 ? `set (${e.size} bytes)` : 'empty'}</span></div>`
        : vars.length ? `<div class="vars">${vars.map((v) => `<span class="var ${v.set ? '' : 'empty'}" data-tip="${esc(`${v.name}\n${v.set ? 'has a value' : 'empty'}${v.duplicate ? ' · defined more than once (last one wins)' : ''}`)}"><span class="dot"></span>${esc(v.name)}${v.duplicate ? '<span class="dup">×2</span>' : ''}</span>`).join('')}</div>`
          : `<div class="empty" style="padding:12px 0 0">${e.error ? esc(e.error) : 'No variables.'}</div>`;
      return `<div class="card"><div class="env-head"><div><div class="env-path">${esc(e.path)}</div>
          <div class="env-meta"><span><b>${esc(projectName(e.project))}</b></span><span>owner <b>${esc(e.owner || '?')}:${esc(e.group || '?')}</b></span><span>mode <b>${esc(e.mode || '?')}</b>${e.expect_mode ? ` (want ${esc(e.expect_mode)})` : ''}</span><span>changed <b>${ago(e.mod_time)}</b></span>
          ${e.secret_file ? '' : `<span><b>${vars.length}</b> names${empty ? `, <b>${empty}</b> empty` : ''}${e.unparsed_lines ? `, ${e.unparsed_lines} lines not understood` : ''}</span>`}
          ${(e.used_by || []).length ? `<span>loaded by <b>${e.used_by.map(esc).join(', ')}</b></span>` : ''}</div></div>${state}</div>${body}</div>`;
    };
    return `<section class="section"><div class="section-head"><h2>Env files</h2><span class="legend-inline"><span><span class="var" style="padding:2px 6px"><span class="dot"></span>has a value</span></span><span><span class="var empty" style="padding:2px 6px"><span class="dot"></span>empty</span></span></span></div>
        <div class="note" style="margin-bottom:16px"><strong>Names only.</strong> A separate probe process with no network access reads these files, keeps each variable's name and whether it is empty, and throws the value away. This page never receives a value, and neither do the logs.</div>
        ${S.probe.stale ? `<div class="card empty" style="margin-bottom:16px">${badge('warning', 'Probe not running')} Env names come from the probe's snapshot, which is missing or old.</div>` : ''}
        <div class="grid">${files.map(card).join('') || '<div class="card empty">No env files configured.</div>'}</div></section>
      <section class="section"><div class="section-head"><h2>Copies on disk</h2><span class="sub">old .env backups still holding (old) secrets</span></div>
        ${copies.length ? `<div class="card flush"><div class="table-wrap"><table><thead><tr><th>File</th><th>Project</th><th>Owner</th><th>Mode</th><th class="num">Size</th><th>Changed</th></tr></thead><tbody>
          ${copies.map((c) => `<tr><td class="mono" style="overflow-wrap:anywhere">${esc(c.path)}</td><td>${esc(projectName(c.project))}</td><td class="mono">${esc(c.owner)}:${esc(c.group)}</td><td class="mono">${esc(c.mode)}${c.mode && c.mode !== '0600' ? ` ${badge('warning', 'readable by others')}` : ''}</td><td class="num">${fmtBytes(c.size)}</td><td class="nowrap muted">${ago(c.mod_time)}</td></tr>`).join('')}
          </tbody></table></div></div>` : '<div class="card empty">None found.</div>'}</section>`;
  }

  function network() {
    const ports = S.ports || [];
    const certs = S.tls || [];
    const groups = {};
    for (const c of certs) (groups[c.fingerprint || c.host] ||= []).push(c);
    const sites = S.nginx || [];
    const certRows = Object.values(groups).sort((a, b) => a[0].days_left - b[0].days_left).map((g) => {
      const c = g[0];
      const pct = Math.max(0, Math.min(100, (c.days_left / 90) * 100));
      let st;
      if (zeroTime(c.not_after)) st = badge('critical', 'unreachable');
      else if (c.days_left < 7) st = badge('critical', `${c.days_left.toFixed(0)} days`);
      else if (c.days_left < 21) st = badge('warning', `${c.days_left.toFixed(0)} days`);
      else if (!c.valid) st = badge('warning', 'does not verify');
      else st = badge('good', `${c.days_left.toFixed(0)} days`);
      return `<tr><td>${g.map((x) => `<div class="mono">${esc(x.host)}</div>`).join('')}${c.error ? `<div class="sub">${esc(c.error)}</div>` : ''}</td>
        <td>${[...new Set(g.map((x) => projectName(x.project)))].map(esc).join(', ')}</td><td>${esc(c.issuer || '—')}</td>
        <td class="nowrap">${fmtDate(c.not_after)}<div style="width:120px;margin-top:5px"><div class="meter ${c.days_left < 7 ? 'crit' : c.days_left < 21 ? 'warn' : ''}" data-tip="${esc(`${c.days_left.toFixed(1)} of ~90 days left`)}"><span style="width:${pct.toFixed(1)}%"></span></div></div></td><td>${st}</td></tr>`;
    }).join('');
    const reserved = ports.filter((x) => x.project);
    const other = ports.filter((x) => !x.project);
    const portRow = (x) => `<tr class="${x.status === 'down' ? 'dim' : ''}"><td class="num"><b>${x.port}</b></td><td>${x.project ? esc(projectName(x.project)) : '<span class="muted">—</span>'}</td><td>${esc(x.label || '')}</td>
      <td>${x.public ? plain('public') : '<span class="ink2">127.0.0.1 only</span>'}</td><td class="mono">${esc((x.listening || []).join(', ') || '—')}</td><td class="mono">${esc(x.user || '')}</td><td>${portBadge(x)}</td></tr>`;
    return `<section class="section"><div class="section-head"><h2>Ports</h2><span class="sub">the reserved-ports map, checked against what is actually listening</span></div>
        <div class="card flush"><div class="table-wrap"><table><thead><tr><th class="num">Port</th><th>Project</th><th>Purpose</th><th>Expected</th><th>Bound to</th><th>User</th><th>Status</th></tr></thead>
        <tbody>${reserved.map(portRow).join('')}</tbody></table></div></div>
        ${other.length ? `<div class="card flush" style="margin-top:16px"><div class="card-head"><h2>Other listeners</h2><span class="muted small">not in the map</span></div><div class="table-wrap"><table><thead><tr><th class="num">Port</th><th>Project</th><th>Purpose</th><th>Expected</th><th>Bound to</th><th>User</th><th>Status</th></tr></thead><tbody>${other.map(portRow).join('')}</tbody></table></div></div>` : ''}
      </section>
      <section class="section"><div class="section-head"><h2>TLS certificates</h2><span class="sub">read from nginx with a TLS handshake for each hostname</span></div>
        <div class="card flush"><div class="table-wrap"><table><thead><tr><th>Hostnames</th><th>Project</th><th>Issuer</th><th>Expires</th><th>Left</th></tr></thead><tbody>${certRows || '<tr><td colspan="5" class="muted">No hostnames configured.</td></tr>'}</tbody></table></div></div></section>
      <section class="section"><div class="section-head"><h2>nginx requests</h2>
        <span class="legend-inline">${['2xx', '3xx', '4xx', '5xx'].map((n) => `<span><span class="swatch" style="display:inline-block;width:10px;height:8px;border-radius:2px;background:var(--st-${n})"></span>${n}</span>`).join('')}</span></div>
        <div class="card flush"><div class="table-wrap"><table><thead><tr><th>Site</th><th>Project</th><th class="num">Requests 24h</th><th>Per hour</th><th>Status mix</th><th class="num">4xx</th><th class="num">5xx</th><th>Last request</th></tr></thead><tbody>
        ${sites.map((s) => `<tr><td>${s.no_host ? `<span class="ink2">${esc(s.site)}</span><div class="sub">no host field: other sites</div>` : `<span class="mono">${esc(s.site)}</span>`}</td><td>${s.project ? esc(projectName(s.project)) : '<span class="muted">—</span>'}</td>
          <td class="num"><b>${fmtNum(s.total_24h)}</b></td><td>${hourBars(s.per_hour, 'var(--s-vol)', { w: 120, h: 22 })}</td><td>${statusMix(s.status_24h)}</td>
          <td class="num">${fmtNum(s.status_24h[2])}</td><td class="num">${s.status_24h[3] ? `<b>${fmtNum(s.status_24h[3])}</b>` : '0'}</td><td class="nowrap muted">${ago(s.last_seen)}</td></tr>`).join('') || '<tr><td colspan="8" class="muted">No requests counted yet (the probe reads the logs).</td></tr>'}
        </tbody></table></div></div>
        <div class="muted small" style="margin-top:8px">Counts only: request paths are never stored, because query strings can carry tokens. The dashboard's own health checks are left out.${sites.some((s) => s.no_host) ? ' Requests in a log without a $host field are counted as one row; adding "$host" to that log format would split them per site.' : ''}</div>
      </section>`;
  }

  // ---------- theme ----------
  const THEMES = ['auto', 'light', 'dark'];
  const THEME_ICON = {
    auto: '<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" stroke-width="1.5"/><path d="M8 2a6 6 0 0 1 0 12z" fill="currentColor"/></svg>',
    light: '<svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="8" cy="8" r="3" fill="currentColor"/><g stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="M8 1v1.6M8 13.4V15M1 8h1.6M13.4 8H15M3 3l1.1 1.1M11.9 11.9L13 13M3 13l1.1-1.1M11.9 4.1L13 3"/></g></svg>',
    dark: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M13.5 10.2A6 6 0 0 1 5.8 2.5a6 6 0 1 0 7.7 7.7z" fill="currentColor"/></svg>',
  };
  function applyTheme(t) {
    document.documentElement.dataset.theme = t;
    const btn = document.getElementById('theme-btn');
    btn.innerHTML = THEME_ICON[t];
    btn.title = `Theme: ${t}`;
  }
  let theme = 'auto';
  try { theme = localStorage.getItem('vpsdash-theme') || 'auto'; } catch (e) { /* storage blocked */ }
  if (!THEMES.includes(theme)) theme = 'auto';
  applyTheme(theme);
  document.getElementById('theme-btn').addEventListener('click', () => {
    theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
    try { localStorage.setItem('vpsdash-theme', theme); } catch (e) { /* storage blocked */ }
    applyTheme(theme);
    render();
  });

  let resizeTimer;
  window.addEventListener('resize', () => { clearTimeout(resizeTimer); resizeTimer = setTimeout(() => { if (route().tab === 'host') render(); }, 150); });

  refresh();
  setInterval(() => { if (!document.hidden) refresh(); }, REFRESH_MS);
  setInterval(renderChrome, 1000);
})();
