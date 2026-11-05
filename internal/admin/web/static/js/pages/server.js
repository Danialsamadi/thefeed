// The Server page: the machine's CPU and network over a range, RAM and disk,
// who can reach whom, thefeed-server's process and the recent server log
// (GET /api/server, /api/logs/server), under the server's name, version and
// uptime (GET /api/service). Read while the page shows. Pure helpers are in
// board-util.js.
'use strict';
(() => {
  const S = Panel.S;
  const RANGES = { '15m': '15 min', '1h': '1 h', '24h': '24 h', '7d': '7 days' };
  let range = '1h';
  let srv = null, failed = '';    // GET /api/server, and why it isn't there (503 off Linux)
  let svc = null;                 // GET /api/service: version, update, uptime
  let lines = null, logErr = '';  // the recent server log, newest first
  let ticks = 0;

  const error = r => fieldErrors(r.data, [])[''];
  async function load(withLog) {
    const want = range;
    const [r, s, l] = await Promise.all([Panel.api('GET', `/api/server?range=${want}`), Panel.api('GET', '/api/service'),
      withLog && Panel.api('GET', '/api/logs/server?limit=10')]);
    if (r && want === range) [srv, failed] = r.ok ? [r.data, ''] : [null, error(r)];
    if (s?.ok) svc = s.data;
    if (l) [lines, logErr] = l.ok ? [l.data.days.flatMap(d => d.lines), ''] : [null, error(l)];
    Panel.render();
  }
  // The journal is read per request: the log every 30 seconds, not every tick.
  Panel.poll(() => {
    if (S.view === 'server' || (!srv && !failed)) load(!lines && !logErr || ++ticks % 6 === 0);
  }, 5000);
  Panel.acts['s-range'] = b => {
    range = b.dataset.r;
    Panel.render();
    load(false);
  };

  const n0 = v => (v == null ? '–' : Math.round(v));
  const r = v => (v == null ? '–' : rate(v).join(' '));
  const size = b => { const u = b >= 2 ** 30 ? 'GB' : 'MB'; return `${inUnit(b, u)} ${u}`; };
  // A point without a sample has no bar.
  const bars = (points, top) => points.map(v => { const h = barPct(v, top); return h == null ? '<i style="visibility:hidden"></i>' : `<i style="height:${h}%"></i>`; }).join('');
  const axis = () => `<div class="xl" aria-hidden="true">${chartAxis(srv.from, srv.step).map(l => `<span>${l}</span>`).join('')}</div>`;

  function profile() {
    let state = '';
    if (S.conn === 'down') state = `<span class="bad">not answering since ${S.downAt}</span>`;
    else if (svc?.state === 'running') state = svc.started ? `online, up ${upFor(svc.started)}` : 'online';
    else if (svc) state = `<span class="bad">${svc.state === 'paused' ? 'not answering' : 'stopped'}${svc.since ? ` since ${when(svc.since)}` : ''}</span>`;
    const version = svc?.version ? `${esc(svc.version)}${svc.update ? ` · ${esc(svc.latest)} available` : svc.latest ? ' · up to date' : ''}` : '';
    return `<div class="profile"><span class="av" aria-hidden="true">${esc(S.host[0] || '')}</span><h3>${esc(S.host)}</h3>${state ? `<p>${state}</p>` : ''}${version ? `<p>${version}</p>` : ''}</div>`;
  }
  // Each part of a total: a stack and a table with the same swatches.
  function parts(items, total) {
    return `<div class="stack" aria-hidden="true">${items.map(([, b, color], i) => `<i style="${i === items.length - 1 ? 'flex:1' : `width:${barPct(b, total)}%`};background:var(${color})"></i>`).join('')}</div>
      <table class="kv">${items.map(([label, b, color, unit], i) => `<tr><td><span class="sw" style="background:var(${color})"></span>${label}</td><td${i === items.length - 1 ? ' class="muted"' : ''}>${inUnit(b, unit)} ${unit}</td></tr>`).join('')}</table>`;
  }
  const outOf = (o, empty = 'off') => {
    if (!o) return `<td>${empty}</td>`;
    const tried = o.of - o.untried;
    return `<td${tried && !o.reached ? ' class="bad"' : ''}>${o.reached} of ${o.of}${o.untried ? `, ${o.untried} not tried yet` : ''}</td>`;
  };
  function stats() {
    const { cpu, net, ram, disk, reach, process: p } = srv;
    const res = reach.resolvers, today = t => (new Date(t).toDateString() === new Date().toDateString() ? `today, ${when(t)}` : when(t));
    return `<div class="srv-grid">
        <div class="sec"><h3>CPU use ${perStep(srv.step)}</h3>
          <p class="lead">Now <b>${n0(cpu.now)}%</b>. Average <b>${n0(cpu.avg)}%</b>, peak <b>${n0(cpu.peak)}%</b>.</p>
          <div class="chart" aria-hidden="true"><span class="yl" style="top:0">100%</span><span class="yl" style="top:36px">50%</span><span class="grid" style="top:36px"></span>
            <div class="bars">${bars(cpu.points, 100)}</div></div>${axis()}</div>
        <div class="sec"><h3>Network ${perStep(srv.step)}</h3>
          <p class="lead">Out now <b>${r(net.outNow)}</b>, peak <b>${r(net.outPeak)}</b>. In <b>${r(net.inNow)}</b>. Today: <b>${size(net.sentToday)}</b> sent, <b>${size(net.receivedToday)}</b> received.</p>
          <div class="chart" aria-hidden="true"><span class="yl" style="top:0">${r(net.scale)} out</span><span class="yl" style="top:88px">${r(net.scale)} in</span>
            <div class="bars out">${bars(net.out, net.scale)}</div><div class="bars in">${bars(net.in, net.scale)}</div></div>${axis()}</div>
        <div class="sec"><h3>RAM: ${inUnit(ram.total - ram.free, 'MB')} of ${inUnit(ram.total, 'MB')} MB</h3>
          ${parts([['thefeed-server', ram.server, '--accent', 'MB'], ['Media cache', ram.mediaCache, '--dim', 'MB'], ['System', ram.system, '--track-edge', 'MB'], ['Free', ram.free, '--surface2', 'MB']], ram.total)}</div>
        <div class="sec"><h3>Disk: ${inUnit(disk.total - disk.free, 'GB')} of ${inUnit(disk.total, 'GB')} GB</h3>
          ${parts([['Reports and logs', disk.reportsLogs, '--dim', 'GB'], ['System, sessions, data', disk.systemData, '--track-edge', 'GB'], ['Free', disk.free, '--surface2', 'GB']], disk.total)}</div>
        <div class="sec"><h3>Who can reach whom</h3>
          <table class="kv"><tr><td>Public resolvers reaching this server</td>${res ? outOf({ reached: res.answered, of: res.asked, untried: 0 }) : '<td>not asked yet</td>'}</tr>
            <tr><td>Telegram data centres from here</td>${outOf(reach.telegram)}</tr>
            <tr><td>Nitter instances from here</td>${outOf(reach.nitter)}</tr>
            <tr><td>GitHub API from here</td>${!reach.github ? '<td>off</td>' : reach.github.ok ? '<td>answering</td>' : '<td class="bad">not answering</td>'}</tr></table></div>
        <div class="sec"><h3>Process</h3>
          <table class="kv"><tr><td>Running since</td><td${p.running ? '' : ' class="bad"'}>${p.running && p.since ? today(p.since) : 'not running'}</td></tr>
            <tr><td>Load, 1 / 5 / 15 min</td><td>${p.load.map(x => x.toFixed(2)).join(' / ')}</td></tr>
            <tr><td>Open sockets</td><td>${p.sockets == null ? '–' : p.sockets.toLocaleString('en')}</td></tr>
            <tr><td>Listening</td><td>${esc(p.listen || '–')}${p.redirect ? `, from ${esc(p.redirect)}` : ''}</td></tr></table></div>
      </div>`;
  }
  function log() {
    const body = logErr ? `<p class="empty">${esc(logErr)}</p>` : !lines ? '<p class="empty">Loading the log…</p>' : !lines.length ? '<p class="empty">Nothing in the server log yet.</p>'
      : `<div class="loglines">${lines.map(l => `<p${{ ERROR: ' class="lv-err"', WARN: ' class="lv-warn"' }[l.level] || ''}>${hms(l.at)} ${esc(l.text)}</p>`).join('')}</div>`;
    return `<div class="sec"><h3>Recent server log</h3><p class="lead">Each feature's own log opens from its row on the Board.</p>${body}</div>`;
  }

  Panel.view('server', {
    loading: 'server stats',
    render() {
      if (!srv && !failed) return null;
      return profile()
        + `<div class="seg" role="group" aria-label="Time range">${Object.entries(RANGES).map(([k, label]) => `<button type="button" data-act="s-range" data-r="${k}" data-k="r-${k}" aria-pressed="${range === k}">${label}</button>`).join('')}</div>`
        + (srv ? stats() : `<div class="state" role="status"><b>No CPU, memory, disk or network figures here</b><span>${esc(failed)}</span></div>`)
        + log();
    },
  });
})();
