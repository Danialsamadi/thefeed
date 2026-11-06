// The Board: every feature's state, one fact and 60 days of uptime, under
// the server's CPU, RAM, disk and network (GET /api/board and
// /api/server/pinned, polled together), and each feature's status page
// (GET /api/features/{id} and its log, /api/logs/{id}) with Check now and,
// for X, the Nitter instance editor, which saves through Control's queue.
// It also gives Control the Board's view of a feature and its check
// (Panel.feature and Panel.check, see control.js). Pure helpers are in
// board-util.js.
'use strict';
(() => {
  const S = Panel.S, root = document.getElementById('app');
  let board = null, failed = '';   // GET /api/board, and why it couldn't load
  let pinned = null;               // GET /api/server/pinned; null where it isn't available
  const pages = {};                // id → GET /api/features/{id}, or {failed}
  const logs = {};                 // id → {filter, days, error}; days null while loading
  const checking = {}, said = {};  // a check running; a line to show instead of the last check's
  let shown = null;                // the page drawn last: a new one slides in
  let edit = null;                 // the Nitter editor while open: {value, err, busy}
  let ticks = 0;

  // The API's states are ok, slow, down and off; the mockup's classes call slow warn.
  // A Nitter instance resting after HTTP 429 is amber too.
  const cls = s => (s === 'slow' || s === 'resting' ? 'warn' : s);
  const STATUS = { ok: 'OK', slow: 'Slow', down: 'Down', pending: 'Not checked', resting: 'Resting' };
  const LEVEL = { ERROR: 'down', WARN: 'warn' };
  const sentence = s => (/[.!?…]$/.test(s) ? s : s + '.');
  const known = t => t && !String(t).startsWith('0001'); // a zero time is "not yet"
  const error = r => (r ? fieldErrors(r.data, [])[''] : `Can't reach ${S.host}. Check the connection and try again.`);
  const focus = k => root.querySelector(`.v-board [data-k="${CSS.escape(k)}"]`)?.focus();

  async function loadPage(id) {
    const r = await Panel.api('GET', `/api/features/${id}`);
    if (r?.ok) pages[id] = r.data;
    else if (r && !pages[id]?.id) pages[id] = { failed: error(r) };
  }
  // The journal is read per request, so logs load on open, on a filter
  // change, after a check and every 30 seconds, not every tick.
  async function loadLog(id) {
    const f = logs[id]?.filter || 'all';
    const r = await Panel.api('GET', `/api/logs/${id}?filter=${f}&limit=100`);
    if (r && (logs[id]?.filter || 'all') === f) logs[id] = { filter: f, days: r.ok ? r.data.days : [], error: r.ok ? '' : error(r) };
    Panel.render();
  }

  // One tick: the Board and the pinned line together, the open feature's
  // page, and Telegram channels' while Control shows them (for their cycle times).
  Panel.poll(async () => {
    const open = S.open.board, chan = S.view === 'control' && S.open.control === 'chan' && open !== 'chan';
    if (open && ++ticks % 6 === 0) loadLog(open);
    const [b, p] = await Promise.all([Panel.api('GET', '/api/board'), Panel.api('GET', '/api/server/pinned'), open && loadPage(open), chan && loadPage('chan')]);
    if (b) [board, failed] = b.ok ? [b.data, ''] : [board, error(b)];
    if (p) pinned = p.ok ? p.data : null;
    Panel.render();
  }, 5000);

  // ---------- the Board ----------
  function pin() {
    if (!pinned) return '';
    const cpu = pinned.cpu == null ? '–' : Math.round(pinned.cpu), out = pinned.out == null ? ['–', 'Mb/s'] : rate(pinned.out);
    const ram = inUnit(pinned.ramUsed, 'MB'), disk = inUnit(pinned.diskUsed, 'GB');
    return `<button class="pin" type="button" data-act="view" data-v="server" data-k="pin" aria-label="Server: CPU ${cpu}%, RAM ${ram} MB, disk ${disk} GB, network out ${out.join(' ')}. Open server details.">
      <span><small>CPU</small><b>${cpu}<i>%</i></b></span>
      <span><small>RAM</small><b>${ram}<i>MB</i></b></span>
      <span><small>Disk</small><b>${disk}<i>GB</i></b></span>
      <span><small>Out</small><b>${out[0]}<i>${out[1]}</i></b></span></button>`;
  }
  const row = f => `<button class="row" type="button" data-act="open" data-id="${f.id}" data-k="row-${f.id}"${S.open.board === f.id ? ' aria-current="true"' : ''}>
      <span class="nm">${esc(f.name)}</span>
      <span class="st st-${cls(f.state)}"><span class="sdot" aria-hidden="true"></span>${esc(f.word)}</span>
      <span class="fact">${esc(f.line)}</span>
      <span class="upl"><span class="bars60" aria-hidden="true">${upBars(f.uptime.days).map(c => `<i class="${c}"></i>`).join('')}</span><span class="pct"><span class="sr">Uptime over 60 days </span>${upPct(f.uptime.percent)}</span></span></button>`;
  function list() {
    const group = (states, one, many) => {
      const fs = board.features.filter(f => states.includes(f.state));
      return fs.length ? `<h3 class="group-h">${plural(fs.length, one, many)}</h3><div class="rows">${fs.map(row).join('')}</div>` : '';
    };
    return pin() + group(['down', 'slow'], 'needs attention', 'need attention') + group(['ok'], 'working', 'working') + group(['off'], 'turned off', 'turned off')
      + '<p class="legend pad">Bars: last 60 days, today on the right. Green up, amber slow, red down, grey off.</p>';
  }

  // ---------- a feature's page ----------
  function status(p) {
    const id = p.id, st = cls(p.state), stopped = p.word === 'Stopped' || p.word === 'Not answering', at = stopped ? board.server.since : p.since;
    const since = !known(at) ? '' : stopped ? `since ${when(at)}, with the whole server`
      : st === 'down' || st === 'warn' ? `for ${span(Date.now() - new Date(at))}, since ${when(at)}` : `since ${when(at)}`;
    const busy = checking[id], off = busy || S.conn !== 'live' ? ' disabled' : '';
    const acts = p.actions.map((a, i) => `<button class="btn${i ? ' quiet' : ''}" type="button" data-act="f-${a.id}" data-id="${id}"${a.page ? ` data-p="${a.page}"` : ''} data-k="kb-${a.id}"${off}>${
      busy && a.id === 'check' ? (id === 'chan' ? 'Fetching…' : 'Checking…') : esc(a.label)}</button>`).join('');
    const c = p.checked;
    const line = stopped ? '' : said[id] || (c && !c.pending ? `Checked at ${when(c.at)}: ${sentence(c.summary)}`
      : known(p.lastCheck) ? `Last automatic check ${when(p.lastCheck)}. Next at ${when(p.nextCheck)}.` : '');
    return `<section class="det-status s-${st}" aria-label="Status">
        <p class="st-line"><span class="sdot" aria-hidden="true"></span><b>${esc(p.word)}</b><span>${since}</span></p>
        <p class="impact">${esc(p.impact)}</p>
        <div class="acts">${acts}</div>
        <p class="checked" aria-live="polite">${esc(line)}</p>
      </section>`;
  }
  function editor() {
    const e = edit, busy = e.busy || S.conn !== 'live' ? ' disabled' : '';
    return `<form class="editor" data-form="f-inst" novalidate>
        <label for="f-inst">Nitter instances, one per line</label>
        <textarea id="f-inst" name="value" rows="4" autocapitalize="off" spellcheck="false" data-k="f-inst"${e.err ? ' aria-invalid="true" aria-describedby="f-inst-err"' : ''}>${esc(e.value)}</textarea>
        ${e.err ? `<p class="err" id="f-inst-err" role="alert">${esc(e.err)}</p>` : '<p class="hint">Tried in this order on every check. Saved changes apply after a restart. Also under Control, X accounts.</p>'}
        <div class="acts"><button class="btn quiet" type="button" data-act="f-edit-cancel" data-k="edit-cancel">Cancel</button><button class="btn" type="submit" data-k="edit-save"${busy}>${e.busy ? 'Saving…' : 'Save instances'}</button></div></form>`;
  }
  function deps(p) {
    const d = p.deps;
    if (!d) return '';
    const si = d.cols.indexOf('Status');
    const table = d.rows.length ? `<table class="tbl"><thead><tr>${d.cols.map(c => `<th scope="col">${esc(c)}</th>`).join('')}</tr></thead>
        <tbody>${d.rows.map(r => `<tr>${r.map((v, i) => (i === si ? `<td class="sw-${cls(v)}">${STATUS[v] ?? esc(v)}</td>` : `<td>${esc(v)}</td>`)).join('')}</tr>`).join('')}</tbody></table>`
      : '<p class="empty">None yet.</p>';
    const at = p.checked?.at || p.lastCheck;
    return `<section class="dsec"><div class="dh"><h3>${esc(d.title)}</h3>${p.id === 'x' && known(at) ? `<span>checked ${when(at)}</span>` : ''}</div>${table}${p.id === 'x' && edit ? editor() : ''}</section>`;
  }
  function incidents(p) {
    const u = p.uptime;
    if (!p.incidents.length) return `<p class="empty">No incidents ${u.off && u.off < 60 ? 'since it was turned on' : 'in the last 60 days'}.</p>`;
    return `<table class="tbl"><thead><tr><th scope="col">Started</th><th scope="col">Lasted</th><th scope="col">What happened</th></tr></thead>
      <tbody>${p.incidents.map(i => `<tr><td>${when(i.start)}</td><td class="sw-${cls(i.worst)}">${lasted(i.start, i.end)}</td><td>${esc(i.summary)}</td></tr>`).join('')}</tbody></table>`;
  }
  function log(p) {
    const L = logs[p.id] || {}, f = L.filter || 'all';
    let body;
    if (L.error) body = `<p class="empty">${esc(L.error)}</p>`;
    else if (!L.days) body = '<p class="empty">Loading the log…</p>';
    else if (!L.days.length) body = `<p class="empty">${f === 'bad' ? 'No warnings or errors in this log.' : 'Nothing in this log yet.'}</p>`;
    else {
      body = `<div class="log" role="log" aria-label="${esc(p.name)} log, newest first">${L.days.map(d => `<p class="lday">${dayLabel(d.day)}</p>${d.lines.map(l =>
        `<p class="ll"><span class="lt">${hms(l.at)}</span><span class="lv ${LEVEL[l.level] || ''}">${esc(l.level)}</span><span class="lm">${esc(l.text)}</span></p>`).join('')}`).join('')}</div>`;
    }
    return `<section class="dsec full"><h3>Log</h3>
        <div class="seg" role="group" aria-label="Log lines">
          <button type="button" data-act="f-filter" data-f="all" data-k="f-all" aria-pressed="${f === 'all'}">All lines</button>
          <button type="button" data-act="f-filter" data-f="bad" data-k="f-bad" aria-pressed="${f === 'bad'}">Warnings and errors</button></div>
        ${logExport(p.id, f)}${body}</section>`;
  }
  function page(id) {
    const f = board?.features.find(f => f.id === id), p = pages[id];
    if (!f) return null;
    const head = { title: f.name, sub: f.line };
    if (!p) return { ...head, html: Panel.loading(`${esc(f.name)}'s status`) };
    if (p.failed) return { ...head, html: `<div class="state" role="alert"><b>Couldn't load ${esc(f.name)}</b><span>${esc(p.failed)}</span></div>` };
    const enter = shown !== id;
    shown = id;
    const u = p.uptime;
    return { ...head, sub: p.line, html: `<div class="det${enter ? ' enter' : ''}">
        ${status(p)}
        ${p.facts.length ? `<section class="dsec full"><h3>Right now</h3><dl class="facts">${p.facts.map(x => `<div><dt>${esc(x.label)}</dt><dd${x.state ? ` class="${cls(x.state)}"` : ''}>${esc(x.value)}</dd></div>`).join('')}</dl></section>` : ''}
        <section class="dsec"><h3>Uptime, last 60 days</h3>
          <div class="uptime" aria-hidden="true">${upBars(u.days).map(c => `<i class="${c}"></i>`).join('')}</div>
          <div class="axis" aria-hidden="true">${uptimeAxis().map(l => `<span>${l}</span>`).join('')}</div>
          <p class="legend">${upLegend(u)}.</p></section>
        ${deps(p)}
        <section class="dsec full"><h3>Incidents</h3>${incidents(p)}</section>
        ${log(p)}
      </div>` };
  }

  Panel.view('board', {
    loading: 'the board',
    render: () => (board ? list() : failed ? `<div class="state" role="alert"><b>Couldn't load the board</b><span>${esc(failed)}</span></div>` : null),
    count: () => board?.attention || 0,
    detail: page,
    open(id) {
      [edit, shown] = [null, null];
      delete said[id];
      logs[id] = { filter: 'all', days: null };
      loadPage(id).then(Panel.render);
      loadLog(id);
    },
    escape() {
      if (!edit) return false;
      closeEditor();
      return true;
    },
  });

  // ---------- for Control (see control.js) ----------
  // While the server is stopped or silent every feature is down with it; that
  // isn't the feature's own failure, so its status is then the server's.
  Panel.feature = id => {
    const f = board?.features.find(f => f.id === id);
    if (!f) return null;
    const out = { status: board.server.state === 'running' ? f.state : board.server.state, line: f.line };
    if (id === 'chan' && pages.chan?.deps?.rows.length) {
      out.last = pages.chan.deps.rows[0][0];
      out.next = pages.chan.facts.find(x => x.label === 'Next cycle')?.value;
    }
    return out;
  };
  // Check now. Telegram channels' is Fetch now: the cycle takes minutes, so
  // it answers 202 and the feature's page says when it's back.
  Panel.check = async id => {
    checking[id] = true;
    delete said[id];
    Panel.render();
    const r = await Panel.api('POST', `/api/checks/${id}`, {});
    let c = r?.ok ? r.data : null;
    while (c?.pending) {
      await new Promise(done => setTimeout(done, 3000));
      const f = await Panel.api('GET', `/api/features/${id}`);
      if (f?.ok) [pages[id], c] = [f.data, f.data.checked];
      else if (f) break;
    }
    checking[id] = false;
    const line = c && !c.pending ? `Checked at ${when(c.at)}: ${sentence(c.summary)}` : r && !r.ok ? error(r) : '';
    if (r && !r.ok) said[id] = line;
    await Promise.all([loadPage(id), S.open.board === id && loadLog(id)]);
    Panel.render();
    return line;
  };

  // ---------- actions ----------
  Panel.acts['f-check'] = async b => {
    await Panel.check(b.dataset.id);
    if (!root.contains(document.activeElement)) focus('kb-check');
  };
  Panel.acts['f-page'] = b => {
    S.view = 'control';
    Panel.acts.open({ dataset: { id: b.dataset.p } });
  };
  Panel.acts['f-filter'] = b => {
    const id = S.open.board;
    logs[id] = { filter: b.dataset.f, days: null };
    Panel.render();
    loadLog(id);
  };

  // The Nitter editor shows the list as Control last read it, and saves one
  // list change at a time through Control's queue, each with the newest base.
  const saved = () => Ctl.C.st.lists.instances.items.map(it => it.name);
  Panel.acts['f-edit'] = async () => {
    if (!Ctl.C.st) await Ctl.call('GET', '/api/settings');
    if (!Ctl.C.st) {
      said.x = "Couldn't read the Nitter instances. Try again.";
      return Panel.render();
    }
    edit = { value: saved().join('\n'), err: '', busy: false };
    Panel.render();
    focus('f-inst');
  };
  function closeEditor() {
    edit = null;
    Panel.render();
    focus('kb-edit');
  }
  Panel.acts['f-edit-cancel'] = closeEditor;
  Panel.forms['f-inst'] = async form => {
    const want = [...new Set(form.elements.value.value.split('\n').map(s => s.trim()).filter(Boolean).map(instanceName))];
    if (!want.length) {
      edit.err = 'Add at least one instance, or remove every X account to turn X off.';
      Panel.render();
      return focus('f-inst');
    }
    Object.assign(edit, { busy: true, err: '' });
    Panel.render();
    let op, r;
    for (let n = 0; n < 50 && (op = nextInstanceOp(saved(), want)); n++) {
      r = await Ctl.call('POST', '/api/lists/instances', op);
      if (!r?.ok) break;
      if (op.op === 'add') want[want.indexOf(op.value)] = saved().at(-1); // as the server stored it
    }
    if (op) {
      Object.assign(edit, { busy: false, err: r?.ok ? "Couldn't save every change. Check the list and save again." : error(r) });
      Panel.render();
      return focus('f-inst');
    }
    edit = null;
    said.x = `Saved ${plural(want.length, 'Nitter instance', 'Nitter instances')}. They apply after a restart.`;
    Panel.render();
    focus('kb-check');
  };
})();
