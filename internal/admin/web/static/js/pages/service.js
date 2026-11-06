// Control, Server, Service and power: whether the server answers, the
// process, start on boot, the version with update and roll back, and the
// machine. Disruptive actions confirm in place, restart in a popup, and say
// what readers lose.
// While the server is silent or stopped, every top-level screen shows a
// banner with the way back.
'use strict';
(() => {
  const S = Panel.S;
  let svc = null;     // GET /api/service; null until it loads
  let failed = '';    // why it couldn't load
  let confirm = '';   // the action whose confirm replaces its row
  let typed = '';     // the domain typed to confirm a shutdown
  let busy = '';      // the action running from this page
  let said = null;    // [text, bad]: how the last action went, until the next one
  let srcErr = '';    // the release source form's error
  // The readers' import link holds the key: fetched when the page opens, so
  // Copy writes it within the click, as Safari wants; never polled.
  let link = '', linkErr = '', copied = '', copiedT; // copied: '', 'ok' or 'fail'

  // Each action's confirm: q, why and go; danger tints it red, typed asks for
  // the domain. Without q it runs at once. busy is the status line meanwhile.
  const C = (a, w = svcWords(svc ?? {})) => ({
    pause: { q: 'Stop answering queries?', why: 'The server stays up but goes silent: readers get no answers and Telegram tunnels close. Resume any time; a restart always comes back answering.', go: 'Stop answering', busy: 'Stopping answers…' },
    resume: { busy: 'Resuming…' },
    restart: { q: 'Restart thefeed-server now?', why: 'Readers lose about 10 seconds of queries. Open Telegram tunnels reconnect on their own.', go: 'Restart now' },
    stop: { q: 'Stop thefeed-server?', why: 'Everything stops: feed, messenger, tunnels and relays. This panel keeps running so you can start it again.', go: 'Stop server', danger: true, busy: 'Stopping…' },
    start: { busy: 'Starting…' },
    'boot-on': { busy: 'Turning start on boot on…' },
    'boot-off': { busy: 'Turning start on boot off…' },
    reboot: { q: 'Reboot the machine?', why: 'Readers get no answers for about a minute. This panel reconnects when the machine is back.', go: 'Reboot', busy: 'Rebooting the machine…' },
    shutdown: { q: 'Shut down the machine?', why: "Everything stops, this panel too. Only your hosting provider's console can turn it on again.", go: 'Shut down', danger: true, typed: true, busy: 'Shutting down…' },
    update: { q: `Update to ${svc?.latest} now?`, why: `${w.update} Readers lose about 10 seconds of queries. You can roll back afterwards.`, go: 'Update now',
      busy: `Updating to ${svc?.latest}: ${w.updating}…` },
    rollback: { q: 'Roll back to the previous version?', why: `${w.rollback} Readers lose about 10 seconds of queries.`, go: 'Roll back', busy: `Rolling back to ${svc?.previous}…` },
    'fix-unit': { q: 'Move these flags into thefeed.env and restart?', why: 'Readers lose about 10 seconds of queries. The old unit is kept as thefeed-server.service.bak in the data directory.',
      go: 'Move and restart', busy: 'Moving the flags, then restarting…' },
  }[a]);

  async function load() {
    const r = await Panel.api('GET', '/api/service');
    if (!r) return;
    if (r.ok) [svc, failed] = [r.data, ''];
    else failed = r.data.errors?.[0]?.message || "Couldn't read thefeed-server's state.";
    Panel.render();
  }
  Panel.poll(load, 5000);

  async function run(a, body = {}) {
    [confirm, busy, said] = ['', a, null];
    Panel.render();
    document.querySelector('.v-control [data-k="svc-status"]')?.focus();
    const r = await Panel.api('POST', `/api/service/${a}`, body);
    busy = '';
    if (r?.ok) svc = r.data;
    if (r?.ok && a === 'shutdown') said = ["The machine is shutting down. Only your hosting provider's console turns it back on.", false];
    if (r && !r.ok) {
      said = [r.data.errors?.[0]?.message || "The panel couldn't do that. Try again.", true];
      load();
    }
    Panel.render();
  }

  const since = (word = 'since') => (svc.since ? ` ${word} ${when(svc.since)}` : '');
  const domainOK = () => !!svc?.domain && typed.trim().toLowerCase() === svc.domain.toLowerCase();

  // The process is up but silent, or stopped: say so on every top-level
  // screen, with the way back.
  Panel.banners.push(where => {
    if (!svc || where === 'section' || S.conn !== 'live' || svc.state === 'running') return '';
    const paused = svc.state === 'paused', a = paused ? 'resume' : 'start';
    return `<div class="banner warnb" role="status"><b>${paused ? `Not answering${since()}.` : `thefeed-server stopped${since('at')}.`}</b>
      <div class="row2"><span>Readers get no answers${paused ? ', though the process is still up' : ''}.</span>
      <button class="btn quiet" type="button" data-act="svc" data-a="${a}" data-k="svcq-${where}"${busy ? ' disabled' : ''}>${busy === a ? esc(C(a).busy) : paused ? 'Resume answering' : 'Start server'}</button></div></div>`;
  });

  function confirmRow(a) {
    const c = C(a), ok = !c.typed || domainOK();
    return `<div class="it confirm${c.danger ? ' danger' : ''}"><div class="tx"><b>${esc(c.q)}</b><small>${c.why}</small>
        ${c.typed ? `<label class="hint" for="svc-typed">Type ${esc(svc.domain)} to confirm</label><input class="inp" id="svc-typed" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" value="${esc(typed)}" data-k="svc-typed">` : ''}</div>
      <div class="acts"><button class="btn quiet" type="button" data-act="svc-cancel" data-k="svc-cancel">Cancel</button><button class="btn${c.danger ? ' danger' : ''}" type="button" data-act="svc-go" data-a="${a}" data-k="svc-go"${ok ? '' : ' disabled'}>${c.go}</button></div></div>`;
  }

  function page() {
    if (!svc) return failed ? `<div class="state" role="alert"><b>Couldn't read thefeed-server's state</b><span>${esc(failed)}</span></div>` : Panel.loading('the service');
    const st = {
      running: ['ok', 'Running', `${svc.listen ? `Answering on ${esc(svc.listen)}. ` : ''}Readers get the feed, messenger and tunnels.`],
      paused: ['warn', 'Not answering', 'The process is up but silent. Readers get no answers.'],
      stopped: ['down', 'Stopped', 'Nothing is running. Readers get no answers.'],
    }[svc.state];
    const off = busy || S.conn !== 'live' ? ' disabled' : '';
    // one row per thing; its actions sit together, and a confirm replaces the row
    const row = (title, sub, acts) => acts.some(([a]) => a === confirm) ? confirmRow(confirm)
      : `<div class="it"><div class="tx"><b>${title}</b><small>${sub}</small></div><div class="acts">${acts.map(([a, label, cls = 'quiet']) =>
        `<button class="btn${cls ? ` ${cls}` : ''}" type="button" data-act="svc" data-a="${a}" data-k="svc-${a}"${off}>${busy === a ? esc(C(a).busy) : label}</button>`).join('')}</div></div>`;
    const line = busy ? C(busy).busy : said ? said[0] : svc.last ? `${svc.last.text} at ${when(svc.last.at)}.` : '';

    const w = svcWords(svc), v = esc(svc.version), prev = esc(svc.previous);
    const version = row(`${v || 'thefeed-server'} installed`,
      (svc.update ? `${esc(svc.latest)} is available. Updating restarts the server.` : svc.latest ? 'The newest release.' : `Couldn't check ${esc(svc.source)} for a newer release.`)
        + (prev ? ` If something broke, go back to ${prev}.` : ''),
      [...(svc.update ? [['update', 'Update…']] : []), ...(prev ? [['rollback', `Roll back to ${prev}…`]] : [])]);
    const source = `<form class="addf" data-form="svcsrc" novalidate><label for="svc-src">Release source</label>
        <div class="addrow"><input class="inp" id="svc-src" name="repo" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" value="${esc(svc.source)}" data-k="svc-src" aria-describedby="svc-src-h"${srcErr ? ' aria-invalid="true"' : ''}${off}>
        <button class="btn quiet" type="submit" data-k="svc-src-go"${off}>Save</button></div>
        <p class="${srcErr ? 'err' : 'hint'}" id="svc-src-h"${srcErr ? ' role="alert"' : ''}>${srcErr ? esc(srcErr) : 'The GitHub owner/repo updates come from. Leave it empty for the one the installer used.'}</p></form>`;

    return `<section class="det-status s-${st[0]}" aria-label="Service status" tabindex="-1" data-k="svc-status">
        <p class="st-line"><span class="sdot" aria-hidden="true"></span><b>${st[1]}</b><span>${since().trim()}</span></p>
        <p class="impact">${st[2]}</p>
        <p class="checked${said?.[1] ? ' err' : ''}" role="status">${esc(line)}</p></section>`
      + (svc.shadowing.length ? Panel.sec(w.shadow, row(esc(svc.shadowing.join(', ')), w.shadowWhy,
        svc.fixUnit ? [['fix-unit', 'Move into thefeed.env…']] : [])) : '')
      + Panel.sec(w.title,
        (svc.state === 'running' ? row('Answering queries', 'Yes. Stopping keeps the process up but silent.', [['pause', 'Stop answering…']])
          : svc.state === 'paused' ? row('Answering queries', `No${svc.since ? `,${since()}` : ''}. Readers get no answers.`, [['resume', 'Resume answering', '']]) : '')
        + (svc.state === 'stopped' ? row('Process', `Stopped${since()}. Starting applies saved changes.`, [['start', 'Start server', '']])
          : row('Process', `${svc.started ? `Up ${upFor(svc.started)}` : 'Up'}. Restarting applies saved changes.`, [['restart', 'Restart…'], ['stop', 'Stop…']]))
        + `<div class="it"><div class="tx"><b id="svc-boot">${w.boot}</b><small>${svc.boot ? w.bootOn :'After a reboot it stays stopped until you start it.'}</small></div>
          <span class="onoff" aria-hidden="true">${svc.boot ? 'On' : 'Off'}</span>
          <button class="sw-btn" type="button" role="switch" aria-checked="${svc.boot}" aria-labelledby="svc-boot" data-act="svc-boot" data-k="sw-boot"${off}><span class="tr"></span></button></div>`)
      + Panel.sec('Version', version + source)
      + Panel.sec('Machine', row(esc(svc.domain || S.host), "A reboot takes about a minute. After a shutdown, only your hosting provider's console turns it back on.", [['reboot', 'Reboot…'], ['shutdown', 'Shut down…']]))
      + Panel.sec('Readers', copied === 'fail'
        ? `<div class="it"><div class="tx"><b>Import link</b><small class="bad">Couldn't copy. Select the link and copy it by hand.</small><input class="linkbox" readonly value="${esc(link)}" aria-label="Import link" data-k="svc-linkbox"></div></div>`
        : `<div class="it"><div class="tx"><b>Import link</b><small${linkErr ? ' class="bad"' : ''}>${linkErr ? esc(linkErr) : copied === 'ok' ? 'Copied. Paste it into the app under Settings, Configs.' : 'For readers: domain, key and server key in one link.'}</small></div>
          <button class="btn quiet" type="button" data-act="svc-copy" data-k="svc-copy">${copied === 'ok' ? 'Copied' : 'Copy link'}</button></div>`);
  }

  function cancel() {
    const a = confirm;
    confirm = '';
    Panel.render();
    document.querySelector(`.v-control [data-k="svc-${a}"]`)?.focus();
  }

  Panel.section({
    group: 'Server', id: 'svc', title: 'Service and power',
    summary() {
      if (!svc) return '';
      if (svc.state === 'running') return `Running${svc.version ? ` ${svc.version}` : ''}${svc.started ? `, up ${upFor(svc.started)}` : ''}${svc.update ? ', update available' : ''}`;
      return [`${svc.state === 'paused' ? 'Not answering' : 'Stopped'}${since()}`, 'bad'];
    },
    open() {
      load();
      loadLink();
    },
    render: page,
    escape() {
      if (!confirm) return false;
      cancel();
      return true;
    },
  });

  Panel.acts.svc = b => {
    const a = b.dataset.a;
    if (a === 'restart') return openRestart();
    if (!C(a).q) return run(a);
    [confirm, typed] = [a, ''];
    Panel.render();
    document.querySelector(`.v-control [data-k="${C(a).typed ? 'svc-typed' : 'svc-go'}"]`)?.focus();
  };
  Panel.acts['svc-go'] = b => {
    const a = b.dataset.a;
    run(a, a === 'shutdown' ? { domain: typed } : {});
  };
  Panel.acts['svc-cancel'] = cancel;
  Panel.acts['svc-boot'] = () => run(svc.boot ? 'boot-off' : 'boot-on');

  // ---------- the restart popup ----------
  // Restart…, here or on the notice of changes waiting, opens a popup over
  // whatever is on screen: it confirms, restarts, then waits up to 30 s for
  // the server to answer again (its control socket says what it listens on).
  const dlg = document.createElement('dialog');
  dlg.className = 'dlg';
  dlg.tabIndex = -1;
  dlg.setAttribute('aria-labelledby', 'rs-q');
  dlg.setAttribute('aria-describedby', 'rs-line');
  dlg.innerHTML = '<p class="st-line"><span class="sdot" aria-hidden="true"></span><b id="rs-q"></b></p><p class="impact" id="rs-line" role="status"></p><div class="acts"></div>';
  document.getElementById('app').append(dlg);
  let rs = { step: '', line: '' }, opener = '';
  const back = () => svc?.state === 'running' && !!svc.listen;

  function drawRestart() {
    const c = C('restart'), s = rs.step;
    const [cls, q] = { ask: ['s-ask', c.q], busy: ['s-busy', 'Restarting…'], done: ['s-ok', 'Back up'],
      late: ['s-warn', 'Not answering yet'], fail: ['s-down', "Couldn't restart"] }[s];
    const btn = (a, label, quiet) => `<button class="btn${quiet ? ' quiet' : ''}" type="button" data-act="${a}" data-k="${a}">${label}</button>`;
    dlg.className = `dlg ${cls}`;
    dlg.querySelector('#rs-q').textContent = q;
    dlg.querySelector('#rs-line').textContent = s === 'ask' ? c.why : rs.line;
    dlg.querySelector('.acts').innerHTML = s === 'ask' ? btn('rs-close', 'Cancel', 1) + btn('rs-go', c.go)
      : s === 'fail' ? btn('rs-close', 'Close', 1) + btn('rs-go', 'Try again') : s === 'busy' ? '' : btn('rs-close', 'Close');
    (dlg.querySelector('[data-k="rs-go"]') || dlg.querySelector('[data-k="rs-close"]') || dlg).focus();
  }

  function openRestart() {
    opener = document.activeElement?.closest('[data-k]')?.dataset.k || '';
    if (rs.step !== 'busy') rs = { step: 'ask', line: '' };
    dlg.showModal();
    drawRestart();
  }

  async function restart() {
    rs = { step: 'busy', line: 'Readers lose about 10 seconds of queries.' };
    drawRestart();
    const t0 = Date.now(), r = await Panel.api('POST', '/api/service/restart', {});
    if (!r?.ok) {
      rs = { step: 'fail', line: r?.data.errors?.[0]?.message || "The panel couldn't reach thefeed-server. Try again." };
      return drawRestart();
    }
    svc = r.data;
    rs.line = 'Restarted. Waiting for it to answer…';
    drawRestart();
    while (!back() && Date.now() - t0 < 30000) {
      await new Promise(f => setTimeout(f, 1000));
      const g = await Panel.api('GET', '/api/service');
      if (g?.ok) svc = g.data;
    }
    rs = back() ? { step: 'done', line: `Answering on ${svc.listen} again. It took ${Math.max(1, Math.round((Date.now() - t0) / 1000))} s.` }
      : { step: 'late', line: "It restarted but hasn't answered for 30 seconds. The Board shows what's failing." };
    Ctl.load();
    Panel.render();
    if (dlg.open) drawRestart();
  }

  Panel.acts['rs-go'] = restart;
  Panel.acts['rs-close'] = () => dlg.close();
  // Esc closes the popup, not the page behind it; while restarting it stays.
  dlg.addEventListener('keydown', e => { if (e.key === 'Escape') e.stopPropagation(); });
  dlg.addEventListener('cancel', e => { if (rs.step === 'busy') e.preventDefault(); });
  dlg.addEventListener('close', () => {
    if (rs.step !== 'busy') rs.step = '';
    [...document.querySelectorAll(`#app [data-k="${CSS.escape(opener)}"]`)].find(el => el.getClientRects().length)?.focus();
  });

  async function loadLink() {
    [link, linkErr, copied] = ['', '', ''];
    const r = await Panel.api('GET', '/api/service/import-link');
    if (r?.ok) link = r.data.link;
    else if (r) linkErr = r.data.errors?.[0]?.message || "Couldn't make the import link.";
    Panel.render();
  }
  // Copied shows for 2.5 s; if the browser won't copy, the link shows in a
  // field to copy by hand.
  Panel.acts['svc-copy'] = async () => {
    if (!link) await loadLink();
    if (!link) return;
    try {
      await navigator.clipboard.writeText(link);
      copied = 'ok';
    } catch {
      copied = 'fail';
    }
    Panel.render();
    clearTimeout(copiedT);
    if (copied === 'ok') copiedT = setTimeout(() => { copied = ''; Panel.render(); }, 2500);
    else document.querySelector('.v-control [data-k="svc-linkbox"]')?.select();
  };
  Panel.forms.svcsrc = async f => {
    const r = await Panel.api('POST', '/api/service/source', { repo: f.elements.repo.value });
    if (r?.ok) {
      [svc, srcErr] = [r.data, ''];
      Panel.say(`Updates now come from ${r.data.source}.`);
    } else if (r) srcErr = r.data.errors?.[0]?.message || "The panel couldn't save that. Try again.";
    Panel.render();
  };
  // The shutdown button waits for the domain, letter for letter.
  document.getElementById('app').addEventListener('input', e => {
    if (e.target.dataset.k !== 'svc-typed') return;
    typed = e.target.value;
    const go = document.querySelector('.v-control [data-k="svc-go"]');
    if (go) go.disabled = !domainOK();
  });
})();
