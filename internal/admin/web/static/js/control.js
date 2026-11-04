// Control's settings pages share this: thefeed-server's settings, lists and
// domains from the settings API (internal/admin/settings), the one queue
// every settings and lists call goes through, the field and list renderers
// the section pages use (pages/chan.js to pages/dns.js), and the notice of
// changes waiting for a restart. Pure helpers are in control-util.js.
//
// Hooks other pages may supply; without them the parts that need them don't show:
//   Panel.feature(id)  the Board's last view of a feature ('x'): {status, line}.
//                      X down shows the "X is down" callout on X accounts.
//                      status is the API's state (ok, slow, down, off), or the
//                      server's ('stopped', 'not answering') while it isn't
//                      running. 'chan' adds last and next, the newest cycle's
//                      start and the next one's, once the Board has read them.
//   Panel.check(id)    run a feature's check now, a promise of the line to show.
//                      'chan' is Fetch all now on Telegram channels.
//   Both are pages/board.js's.
// Restart… in the notice opens Service and power (pages/service.js) in its
// restart confirm, through its own Panel.acts.svc.
'use strict';

const Ctl = (() => {
  const S = Panel.S, root = document.getElementById('app');
  const C = {
    st: null, failed: '',  // GET /api/settings, and why it couldn't load
    domains: null,         // GET /api/domains
    local: {},             // key → a value sent and not answered yet
    err: {},               // field → its error
    row: null, mode: '',   // the open list row "list:name"; '', 'edit' or 'confirm'
    q: '', busy: false,    // the channel search; a list change or discard in flight
  };

  // Every write sends the base of the state the operator is looking at, and
  // a write sent before the last one's answer would carry a stale base and
  // be refused. So calls go one at a time, each with the newest base.
  let queue = Promise.resolve();
  function call(method, path, body) {
    const p = queue.then(async () => {
      const r = await Panel.api(method, path, body && { ...body, base: C.st?.base });
      if (r?.ok && r.data.base) C.st = r.data;
      if (r?.status === 409) load(); // changed elsewhere: show what's there now
      return r;
    });
    queue = p.catch(() => {});
    return p;
  }

  async function load() {
    const [r] = await Promise.all([call('GET', '/api/settings'), Panel.api('GET', '/api/domains').then(d => { if (d?.ok) C.domains = d.data.domains; })]);
    if (r) C.failed = r.ok ? '' : fieldErrors(r.data, [])[''];
    render();
  }
  // Every 10 s, so a restart from Service and power, or an edit over SSH,
  // shows here soon. Not Panel.poll: that holds while live numbers are
  // paused, and a pause mustn't leave a restart's changes "waiting".
  let due = 0;
  setInterval(() => {
    if (S.auth !== 'in' || S.conn !== 'live' || document.hidden || Date.now() < due) return;
    due = Date.now() + 10000;
    load();
  }, 1000);

  // Panel.render waits while a pointer is down, so changing a field and then
  // clicking a button still lands the click.
  const render = () => Panel.render();

  const focus = k => { const el = root.querySelector(`.v-control [data-k="${CSS.escape(k)}"]`); el?.focus(); return el; };
  const error = r => fieldErrors(r.data, [])[''];
  const setting = k => C.st.settings.find(s => s.key === k);
  const val = k => (k in C.local ? C.local[k] : setting(k)?.saved ?? '');
  const values = () => Object.fromEntries(C.st.settings.map(s => [s.key, val(s.key)]));
  const after = (help, k) => (setting(k)?.waiting ? (help ? `${help} · applies after restart` : 'Applies after restart') : help);
  const off = () => (S.conn !== 'live' ? ' disabled' : '');
  const fid = k => 'c-' + k.replace(/\W/g, '-');

  // save sets one setting. It shows at once and is put right by the answer;
  // a value the server refuses stays, marked, for the operator to fix.
  async function save(key, value) {
    C.local[key] = value;
    render();
    const r = await call('PATCH', `/api/settings/${encodeURIComponent(key)}`, { value });
    const e = r && !r.ok ? fieldErrors(r.data, [key]) : {};
    if (e[key]) C.err[key] = e[key];
    else if (C.local[key] === value) delete C.local[key];
    if (e['']) Panel.say(e['']);
    render();
  }

  // ---------- fields ----------
  function sw(key, label, sub) {
    const on = isOn(val(key));
    return `<div class="it"><div class="tx"><b id="${fid(key)}">${label}</b><small>${after(sub, key)}</small></div>
      <span class="onoff" aria-hidden="true">${on ? 'On' : 'Off'}</span>
      <button class="sw-btn" type="button" role="switch" aria-checked="${on}" aria-labelledby="${fid(key)}" data-act="c-sw" data-key="${key}" data-k="sw-${key}"${off()}><span class="tr"></span></button></div>`;
  }
  // Number and text fields commit on change (see the change listener below).
  function field(type, key, label, help, unit) {
    const s = setting(key), id = fid(key), e = C.err[key];
    const input = `<input class="inp${type === 'text' ? ' wide' : ''}" id="${id}" type="${type}" ${type === 'number' ? `inputmode="numeric" min="${s.min ?? 0}" max="${s.max}" step="1"` : 'autocomplete="off" autocapitalize="off" spellcheck="false"'}
      value="${esc(val(key))}" data-key="${key}" data-k="${type[0]}-${key}" aria-describedby="${id}-h"${e ? ' aria-invalid="true"' : ''}${off()}>`;
    return `<div class="it"><div class="tx"><label for="${id}"><b>${label}</b></label><small id="${id}-h"${e ? ' class="bad"' : ''}>${e ? esc(e) : after(help, key)}</small></div>
      ${type === 'number' ? `<span class="numf">${input}<span>${unit}</span></span>` : input}</div>`;
  }
  const num = (key, label, help, unit = '') => field('number', key, label, help, unit);
  const txt = (key, label, help) => field('text', key, label, help);
  function opts(key, label, help) {
    return `<div class="it"><div class="tx"><b id="${fid(key)}">${label}</b><small>${after(help, key)}</small></div>
      <div class="seg inl" role="group" aria-labelledby="${fid(key)}">${setting(key).allowed.map(o =>
        `<button type="button" data-act="c-opt" data-key="${key}" data-v="${o}" data-k="o-${key}-${o}" aria-pressed="${val(key) === o}"${off()}>${o}</button>`).join('')}</div></div>`;
  }
  Panel.acts['c-sw'] = b => save(b.dataset.key, String(!isOn(val(b.dataset.key))));
  Panel.acts['c-opt'] = b => save(b.dataset.key, b.dataset.v);
  root.addEventListener('change', e => {
    const t = e.target, s = C.st && t.tagName === 'INPUT' && setting(t.dataset.key ?? '');
    if (!s) return;
    const v = t.value.trim(), bad = s.type === 'int' ? checkNumber(v, s.min ?? 0, s.max) : '';
    if (bad) {
      C.err[s.key] = bad;
      return render();
    }
    delete C.err[s.key];
    save(s.key, s.type === 'int' ? String(+v) : v);
  });

  // ---------- lists ----------
  const RM = {
    channels: 'Readers stop getting its new posts right away, and the channels after it move up one number.',
    private: 'The server leaves it after a restart.', x: 'Its posts stop updating after a restart.', instances: 'The server stops trying it after a restart.',
  };
  function meta(id, it) {
    if (id === 'private' && !C.st.telegram.loggedIn) {
      return setting('fetch.noTelegram').waiting ? ['Stops updating after a restart: the Telegram login is logged out', 'bad'] : ['Not updating: the Telegram login is logged out', 'bad'];
    }
    if (it.waiting) return [{ channels: 'Fetched after a restart', private: 'Joins after a restart', x: 'Fetched after a restart', instances: 'Tried after a restart' }[id]];
    if (id === 'private') return ['Joined'];
    if (id === 'x' && Panel.feature?.('x')?.status === 'down') return ['Paused: no Nitter instance answers', 'bad'];
    return [''];
  }
  function about(id, it, i, len) {
    if (id === 'channels') return `Readers see it as channel ${it.channel}. Source: <code>t.me/${esc(it.name)}</code>.`;
    if (id === 'private') return `Readers see it as channel ${it.channel}. Invite link <code>${esc(itemName(id, it.name))}</code>, the rest is hidden.`;
    if (id === 'x') return `Readers see it as channel ${it.channel}, named <code>x/${esc(it.name)}</code>.`;
    return `Tried ${i === 0 ? 'first' : `${i + 1} of ${len}`}.${len === 1 ? ' Keep at least one instance.' : ''}`;
  }
  // rows draws a list's entries, or some of them (a search), as rows that
  // open to their actions. Each keeps its place in the whole list.
  function rows(id, items, empty) {
    const all = C.st.lists[id].items, busy = C.busy || S.conn !== 'live' ? ' disabled' : '';
    if (!items.length) return empty ? `<p class="empty pad">${empty}</p>` : '';
    return `<ul class="lst">${items.map(it => {
      const i = all.indexOf(it), key = esc(`${id}:${it.name}`), dk = esc(`${id}-${it.name}`), open = C.row === `${id}:${it.name}`, [m, cls] = meta(id, it);
      let body = '';
      if (open && C.mode === 'edit') {
        const e = C.err.ren;
        body = `<form class="li-b renf" data-form="c-ren" data-row="${key}" novalidate><label for="c-ren">New name</label>
          <input class="inp" id="c-ren" name="value" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" value="${esc(it.name)}" data-k="c-ren"${e ? ' aria-invalid="true" aria-describedby="c-ren-e"' : ''}>
          ${e ? `<p class="err" id="c-ren-e" role="alert">${esc(e)}</p>` : ''}
          <div class="acts"><button class="btn quiet" type="button" data-act="c-cancel" data-k="ren-cancel">Cancel</button><button class="btn" type="submit" data-k="ren-save"${busy}>Save</button></div></form>`;
      } else if (open && C.mode === 'confirm') {
        body = `<div class="li-b confirm"><p><b>Remove ${esc(itemName(id, it.name))}?</b> ${RM[id]}</p>
          <div class="acts"><button class="btn quiet" type="button" data-act="c-cancel" data-k="rm-cancel">Cancel</button><button class="btn danger" type="button" data-act="c-rm-go" data-row="${key}" data-k="rm-go"${busy}>Remove</button></div></div>`;
      } else if (open) {
        const searching = id === 'channels' && C.q.trim(), move = !searching;
        const acts = [(id === 'channels' || id === 'x') && ['c-rename', 'Rename'], move && ['c-up', 'Move up', i === 0], move && ['c-down', 'Move down', i === all.length - 1],
          ['c-rm', 'Remove…', id === 'instances' && all.length === 1]].filter(Boolean);
        body = `<div class="li-b"><p>${about(id, it, i, all.length)}${searching ? ' Clear the search to change the order.' : ''}</p>
          <div class="acts">${acts.map(([a, l, d]) => `<button class="btn quiet" type="button" data-act="${a}" data-row="${key}" data-k="${a}-${dk}"${d ? ' disabled' : busy}>${l}</button>`).join('')}</div></div>`;
      }
      return `<li class="li"><button class="li-h" type="button" data-act="c-row" data-row="${key}" data-k="row-${dk}" aria-expanded="${open}"><span class="pos">${id === 'instances' ? i + 1 : it.channel}</span><span><b>${esc(itemName(id, it.name))}</b>${m ? `<small${cls ? ` class="${cls}"` : ''}>${esc(m)}</small>` : ''}</span><span class="chev" aria-hidden="true"></span></button>${body}</li>`;
    }).join('')}</ul>`;
  }
  function addForm(id, label, ph, hint) {
    const k = 'add-' + id, e = C.err[k], busy = C.busy || S.conn !== 'live' ? ' disabled' : '';
    return `<form class="addf" data-form="c-add" data-list="${id}" novalidate><label for="c-${k}">${label}</label>
      <div class="addrow"><input class="inp" id="c-${k}" name="value" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" placeholder="${ph}" data-k="${k}" aria-describedby="c-${k}-h"${e ? ' aria-invalid="true"' : ''}${off()}><button class="btn" type="submit" data-k="${k}-go"${busy}>Add</button></div>
      <p class="${e ? 'err' : 'hint'}" id="c-${k}-h"${e ? ' role="alert"' : ''}>${e ? esc(e) : hint}</p></form>`;
  }

  // An action's row names its entry, found again now: the list may have
  // changed since it was drawn.
  function entry(el) {
    const [id, ...name] = el.dataset.row.split(':'), items = C.st.lists[id].items, i = items.findIndex(it => it.name === name.join(':'));
    return [id, i, items[i]];
  }
  async function change(id, op, i, more) {
    C.busy = true;
    render();
    const r = await call('POST', `/api/lists/${id}`, { op, index: i, ...more });
    C.busy = false;
    return r;
  }
  const focusRow = () => C.row && focus('row-' + C.row.replace(':', '-'));
  function closeMode() {
    C.mode = '';
    delete C.err.ren;
    render();
    focusRow();
  }

  Panel.acts['c-row'] = b => {
    C.row = C.row === b.dataset.row ? null : b.dataset.row;
    C.mode = '';
    delete C.err.ren;
    render();
  };
  Panel.acts['c-rename'] = () => { C.mode = 'edit'; render(); focus('c-ren')?.select(); };
  Panel.acts['c-rm'] = () => { C.mode = 'confirm'; render(); focus('rm-go'); };
  Panel.acts['c-cancel'] = closeMode;
  Panel.forms['c-add'] = async f => {
    const id = f.dataset.list, k = 'add-' + id, v = f.elements.value.value;
    const r = v.trim() ? await change(id, 'add', 0, { value: v }) : null;
    if (r?.ok) {
      delete C.err[k];
      const l = r.data.list, items = r.data.lists[l].items, it = items[items.length - 1];
      Panel.say(listDone(r.data, 'add', it.name, l === 'instances' ? items.length : it.channel));
      if (id === 'channels') C.q = '';
    } else if (r) C.err[k] = error(r);
    else if (!v.trim()) C.err[k] = 'Type a name or a link first.';
    render();
    const el = focus(k);
    if (el && r?.ok) el.value = '';
  };
  Panel.forms['c-ren'] = async f => {
    const [id, i] = entry(f), r = await change(id, 'rename', i, { value: f.elements.value.value });
    if (r?.ok) {
      const it = r.data.lists[id].items[i];
      Object.assign(C, { row: `${id}:${it.name}`, mode: '' });
      delete C.err.ren;
      Panel.say(listDone(r.data, 'rename', it.name, it.channel));
      render();
      return focusRow();
    }
    if (r) C.err.ren = error(r);
    render();
    focus('c-ren');
  };
  Panel.acts['c-rm-go'] = async b => {
    const [id, i, it] = entry(b), r = await change(id, 'remove', i);
    if (r?.ok) {
      Object.assign(C, { row: null, mode: '' });
      Panel.say(listDone(r.data, 'remove', it.name, 0));
    } else if (r) Panel.say(error(r));
    render();
    if (r?.ok) focus(`add-${id === 'private' ? 'channels' : id}`);
  };
  async function move(b, d) {
    const [id, i] = entry(b), r = await change(id, 'move', i, { to: i + d });
    if (r?.ok) {
      const it = r.data.lists[id].items[i + d];
      Panel.say(listDone(r.data, 'move', it.name, id === 'instances' ? i + d + 1 : it.channel));
    } else if (r) Panel.say(error(r));
    render();
    const same = focus(b.dataset.k);
    if (!same || same.disabled) focusRow();
  }
  Panel.acts['c-up'] = b => move(b, -1);
  Panel.acts['c-down'] = b => move(b, 1);

  // ---------- changes waiting for a restart ----------
  Panel.banners.push(where => {
    if (!C.st?.waiting || S.conn !== 'live' || (where !== 'control' && where !== 'section')) return '';
    const w = where === 'control' ? 'in-list' : 'in-det';
    return `<div class="notice ${w}" role="status"><span>${plural(C.st.waiting, 'change waits', 'changes wait')} for a restart.</span>
      <button class="btn quiet" type="button" data-act="c-discard" data-k="discard-${w}"${C.busy ? ' disabled' : ''}>Discard</button>${Panel.acts.svc
        ? `<button class="btn" type="button" data-act="c-restart" data-k="restart-${w}">Restart…</button>` : ''}</div>`;
  });
  Panel.acts['c-discard'] = async () => {
    const n = C.st.waiting;
    C.busy = true;
    render();
    const r = await call('POST', '/api/settings/discard', {});
    C.busy = false;
    if (r?.ok) {
      Object.assign(C, { local: {}, err: {} });
      Panel.say(`Discarded ${plural(n, 'change', 'changes')}. Everything matches what the server runs.`);
    } else if (r) Panel.say(error(r));
    render();
    (root.querySelector('.v-control [data-k="pmsg"]:not(:empty)') || root.querySelector('.v-control .cl'))?.focus();
  };
  // Service and power's own Restart… button, pressed: it opens its confirm.
  Panel.acts['c-restart'] = () => {
    Panel.acts.open({ dataset: { id: 'svc' } });
    Panel.acts.svc({ dataset: { a: 'restart' } });
  };

  // section registers a Control section drawn from the settings. p is
  // Panel.section's, with render called only once the settings are in.
  function section(p) {
    Panel.section({
      ...p,
      summary: () => (C.st ? p.summary?.() ?? summary(p.id, values(), C.st.lists, C.domains?.length) : ''),
      wait: () => (C.st ? waitingIn(C.st, p.id) : 0),
      open() {
        for (const k in C.err) delete C.local[k];
        Object.assign(C, { row: null, mode: '', q: '', err: {} });
        p.open?.();
        load();
      },
      render: () => (C.st ? p.render()
        : C.failed ? `<div class="state" role="alert"><b>Couldn't load the settings</b><span>${esc(C.failed)}</span></div>` : Panel.loading('the settings')),
      escape() {
        if (!C.mode) return p.escape?.() || false;
        closeMode();
        return true;
      },
    });
  }

  return { C, call, render, focus, error, setting, val, values, after, off, fid, save, sw, num, txt, opts, rows, addForm, section };
})();
