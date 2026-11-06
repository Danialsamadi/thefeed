// The Logs page: thefeed-server's log by service, newest first, with the
// warnings filter, a search, older lines on request, and an export of the
// last hour, day or week as a .log file (GET /api/logs/{service} and
// /api/logs/{service}/export). The newest lines refresh every 10 seconds
// unless older ones are showing.
'use strict';
(() => {
  const S = Panel.S;
  const SERVICES = [['server', 'All of thefeed-server'], ['feed', 'DNS feed'], ['chan', 'Telegram channels'], ['x', 'X (Twitter)'],
    ['chat', 'Messenger'], ['tg', 'Telegram over DNS'], ['dnsm', 'DNS media relay'], ['gh', 'GitHub relay']];
  const LEVEL = { ERROR: 'down', WARN: 'warn' };
  let svc = 'server', filter = 'all', q = '';
  let days = null, next = '', failed = '';
  let busy = false, older = false; // a read in flight; older pages are showing
  let asked = 0, ticks = 0, typing;

  const query = () => `filter=${filter}${q ? `&q=${encodeURIComponent(q)}` : ''}`;

  // more reads the page after the last one shown; otherwise the newest.
  async function load(more) {
    const n = ++asked;
    busy = true;
    const r = await Panel.api('GET', `/api/logs/${svc}?${query()}&limit=200${more ? `&cursor=${encodeURIComponent(next)}` : ''}`);
    if (n !== asked) return;
    busy = false;
    if (r?.ok) {
      days = more ? join(days, r.data.days) : r.data.days;
      [next, failed, older] = [r.data.next, '', more || older];
    } else if (r) failed = fieldErrors(r.data, [])[''];
    Panel.render();
  }
  // join adds older days to days, the first one onto the last when it's the same day.
  function join(days, more) {
    const out = days.map(d => ({ ...d, lines: [...d.lines] }));
    for (const d of more) {
      const last = out[out.length - 1];
      if (last?.day === d.day) last.lines.push(...d.lines);
      else out.push(d);
    }
    return out;
  }
  function reset() {
    [days, next, failed, older] = [null, '', '', false];
    Panel.render();
    load(false);
  }
  Panel.poll(() => {
    if (S.view === 'logs' && !older && !busy && ++ticks % 2 === 0) load(false);
  }, 5000);

  // The filters and the export stay in view above the lines as they scroll.
  function controls() {
    return `<div class="lgtools"><div class="lgbar">
        <select class="inp" data-k="lg-svc" aria-label="Service">${SERVICES.map(([id, name]) => `<option value="${id}"${id === svc ? ' selected' : ''}>${name}</option>`).join('')}</select>
        <div class="seg inl" role="group" aria-label="Log lines">
          <button type="button" data-act="lg-f" data-f="all" data-k="lg-all" aria-pressed="${filter === 'all'}">All lines</button>
          <button type="button" data-act="lg-f" data-f="bad" data-k="lg-bad" aria-pressed="${filter === 'bad'}">Warnings and errors</button></div>
        <input class="inp lgq" type="search" placeholder="Search the log" aria-label="Search the log" autocomplete="off" spellcheck="false" value="${esc(q)}" data-k="lg-q">
      </div>${logExport(svc, filter, q)}</div>`;
  }

  function lines() {
    if (failed) return `<div class="state" role="alert"><b>Couldn't read the log</b><span>${esc(failed)}</span></div>`;
    if (!days) return '<p class="empty lgpad">Loading the log…</p>';
    if (!days.length) {
      return `<p class="empty lgpad">${q ? `No lines with “${esc(q)}”${filter === 'bad' ? ' among the warnings and errors' : ''}.` : filter === 'bad' ? 'No warnings or errors in this log.' : 'Nothing in this log yet.'}</p>`;
    }
    const name = SERVICES.find(([id]) => id === svc)[1];
    const shown = days.reduce((n, d) => n + d.lines.length, 0);
    return `<div class="log lgpad" role="log" aria-label="${esc(name)} log, newest first">${days.map(d => `<p class="lday">${dayLabel(d.day)}</p>${d.lines.map(l =>
        `<p class="ll"><span class="lt">${hms(l.at)}</span><span class="lv ${LEVEL[l.level] || ''}">${esc(l.level)}</span><span class="lm">${esc(l.text)}</span></p>`).join('')}`).join('')}</div>
      <div class="lgbar lgmore"><span>${plural(shown, 'line', 'lines')}${older ? '. Not refreshing while older lines show.' : ', newest first.'}</span>
        ${older ? '<button class="btn quiet" type="button" data-act="lg-newest" data-k="lg-newest">Back to the newest</button>' : ''}
        ${next ? `<button class="btn quiet" type="button" data-act="lg-older" data-k="lg-older"${busy ? ' disabled' : ''}>${busy ? 'Loading…' : 'Load older'}</button>` : ''}</div>`;
  }

  Panel.view('logs', {
    loading: 'the log',
    render() {
      if (!days && !failed && !busy) queueMicrotask(() => load(false));
      return controls() + lines();
    },
  });

  Panel.acts['lg-f'] = b => {
    filter = b.dataset.f;
    reset();
  };
  Panel.acts['lg-older'] = () => load(true);
  Panel.acts['lg-newest'] = reset;
  const root = document.getElementById('app');
  root.addEventListener('change', e => {
    if (e.target.dataset.k !== 'lg-svc') return;
    svc = e.target.value;
    reset();
  });
  // The search waits for a pause in the typing.
  root.addEventListener('input', e => {
    if (e.target.dataset.k !== 'lg-q') return;
    clearTimeout(typing);
    typing = setTimeout(() => {
      q = e.target.value.trim();
      reset();
    }, 400);
  });
})();
