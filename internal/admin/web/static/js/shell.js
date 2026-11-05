// The panel shell: the sign-in screens, the nav, the list and detail panes,
// Control's section list and the states every page shares (can't reach the
// server, first load, paused).
//
// Pages plug in. Each page is one file under js/pages/, loaded by one
// <script defer> in index.html after this file, that registers itself:
//
//   Panel.view('board', {         // 'board' or 'server'
//     loading: 'the board',       // first load: "Loading the board from <host>…"
//     render() {},                // the list pane, or the whole Server page: HTML, or null until loaded
//     detail(id) {},              // board: the open row as {title, sub, html}; rows carry
//                                 //   data-act="open" data-id="<id>" data-k="row-<id>"
//     open(id) {},                // board, optional: runs each time a row opens
//     escape() {},                // board, optional: close an in-place editor; true if it did
//     count() {},                 // board: how many features need attention, shown on the nav
//   });
//   Panel.section({ group: 'Sources', id: 'chan', title: 'Telegram channels',
//     summary() {},               // the line under its title: text, or [text, 'bad']
//     render() {},                // the open section: HTML (use Panel.sec for its groups)
//     open() {},                  // optional: runs each time it opens, e.g. to load data
//     escape() {},                // optional: close an in-place editor; true if it did
//     wait() {},                  // optional: how many of its changes wait for a restart
//   });
//   Panel.acts.name = (button, event) => {}   // clicks on [data-act="name"]
//   Panel.forms.name = form => {}             // submits of form[data-form="name"]
//   Panel.banners.push(where => html)         // more banners under each screen's header;
//                                             // where: 'board', 'server', 'control' or 'section'
//                                             // (a Control section); a Board row's page has none
//   Panel.poll(fn, ms)                        // live updates; held while paused, offline or hidden
//   Panel.api(method, path, body)             // {ok, status, data}, or null when unreachable
//   Panel.render()                            // re-render; keeps focus and anything typed, and
//                                             // waits while a pointer is down
//
// Control's groups come in a fixed order; sections keep their script order.
'use strict';

const Panel = (() => {
  // Icons from internal/web/static/js/icons.js; pause is drawn to match (the set has none).
  const I = {
    board: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8"/><path d="M3 10a2 2 0 0 1 .709-1.528l7-5.999a2 2 0 0 1 2.582 0l7 5.999A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>',
    server: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M11 13H7"/><path d="M19 9h-4"/><path d="M3 3v16a2 2 0 0 0 2 2h16"/><rect x="15" y="5" width="4" height="12" rx="1"/><rect x="7" y="8" width="4" height="9" rx="1"/></svg>',
    control: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M11 10.27 7 3.34"/><path d="m11 13.73-4 6.93"/><path d="M12 22v-2"/><path d="M12 2v2"/><path d="M14 12h8"/><path d="m17 20.66-1-1.73"/><path d="m17 3.34-1 1.73"/><path d="M2 12h2"/><path d="m20.66 17-1.73-1"/><path d="m20.66 7-1.73 1"/><path d="m3.34 17 1.73-1"/><path d="m3.34 7 1.73 1"/><circle cx="12" cy="12" r="2"/><circle cx="12" cy="12" r="8"/></svg>',
    back: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M13 9a1 1 0 0 1-1-1V4.707a.707.707 0 0 0-1.207-.5l-6.94 6.94a1.207 1.207 0 0 0 0 1.707l6.94 6.94a.707.707 0 0 0 1.207-.5V16a1 1 0 0 1 1-1h2a1 1 0 0 0 1-1v-4a1 1 0 0 0-1-1z"/><path d="M20 9v6"/></svg>',
    play: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 5a2 2 0 0 1 3.008-1.728l11.997 6.998a2 2 0 0 1 .003 3.458l-12 7A2 2 0 0 1 5 19z"/></svg>',
    pause: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="6" y="4" width="4" height="16" rx="1"/><rect x="14" y="4" width="4" height="16" rx="1"/></svg>',
  };
  const NAV = [['board', 'Board'], ['server', 'Server'], ['control', 'Control']];
  const GROUPS = ['Server', 'Sources', 'Features', 'Server settings', 'This panel'];

  const S = {
    auth: 'boot',                // boot, setup, login or in
    me: null,                    // {username, since} of this session
    host: location.hostname,
    view: 'board',
    open: { board: null, control: null },
    // Live from the start, reduced motion or not: a number that changes
    // isn't motion (the CSS drops the animations), and a paused Board goes
    // stale beside the feature pages, which load fresh.
    paused: false,
    conn: 'live', downAt: '', retrying: false, retryMsg: '',
    theme: 'dark',
    err: {}, busy: '', lockedUntil: 0, shown: {}, authMsg: '', flash: '',
    msg: {},                     // a message per Control section, shown at its top
  };
  const views = {}, sections = [], polls = [], acts = {}, forms = {}, banners = [];

  const root = document.getElementById('app');
  const $ = sel => root.querySelector(sel);
  const visible = sel => [...root.querySelectorAll(sel)].find(el => el.getClientRects().length);

  // ---------- API ----------
  async function api(method, path, body) {
    let r;
    try {
      r = await fetch(path, body === undefined ? { method, cache: 'no-store' }
        : { method, cache: 'no-store', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    } catch {
      if (S.conn !== 'down') {
        Object.assign(S, { conn: 'down', downAt: when(new Date()), retryMsg: '' });
        draw();
      }
      return null;
    }
    const data = await r.json().catch(() => ({}));
    if (S.conn === 'down') { Object.assign(S, { conn: 'live', retryMsg: '' }); polls.forEach(p => { p.due = 0; }); draw(); }
    if (r.status === 401 && S.auth === 'in') signOut('Your session ended. Log in again.');
    return { ok: r.ok, status: r.status, data };
  }

  // Live updates: each poll runs once at once, then every ms while live.
  setInterval(() => {
    if (S.auth !== 'in' || S.conn !== 'live' || document.hidden) return;
    const now = Date.now();
    for (const p of polls) if (now >= p.due && !(S.paused && p.due)) { p.due = now + p.ms; p.fn(); }
  }, 1000);
  // While unreachable, try again on its own every 30 seconds.
  setInterval(() => { if (S.conn === 'down' && !S.retrying) reconnect(); }, 30000);
  async function reconnect() {
    S.retrying = true;
    render();
    const r = await api('GET', '/api/auth/session');
    S.retrying = false;
    if (!r) S.retryMsg = 'Still no answer. Trying again on its own every 30 s.';
    if (r && S.auth === 'boot') return boot();
    render();
  }

  // ---------- sign-in ----------
  async function boot() {
    render();
    const s = await api('GET', '/api/auth/setup'); // the sign-in page's reset line, for after a sign-out too
    if (!s) return;
    S.reset = s.data.reset;
    const r = await api('GET', '/api/auth/session');
    if (!r) return;
    if (r.ok) return signIn(r.data);
    S.auth = s.data.needed ? 'setup' : 'login';
    render();
    $('.auth input:not([disabled])')?.focus();
  }
  function signIn(me, flash = '') {
    Object.assign(S, { auth: 'in', me, err: {}, shown: {}, authMsg: '', flash });
    polls.forEach(p => { p.due = 0; });
    render();
  }
  function signOut(msg) {
    Object.assign(S, { auth: 'login', me: null, authMsg: msg, err: {}, shown: {}, msg: {}, flash: '', view: 'board', open: { board: null, control: null } });
    render();
    $('#login-username')?.focus();
  }
  const locked = () => Date.now() < S.lockedUntil;
  let unlock;
  function lock(secs) {
    S.lockedUntil = Date.now() + secs * 1000;
    clearTimeout(unlock);
    unlock = setTimeout(() => { Object.values(S.err).forEach(e => delete e['']); render(); }, secs * 1000);
  }

  // submit posts one of the sign-in forms (setup, login, change password) and
  // puts any errors next to their fields. Typed values survive the error,
  // except passwords that were wrong. Returns the API's answer.
  async function submit(name, path, fields) {
    const form = () => $(`form[data-form="${name}"]`);
    const body = Object.fromEntries(fields.map(n => [n, form().elements[n].value]));
    S.busy = name;
    S.err[name] = {};
    render();
    const r = await api('POST', path, body);
    S.busy = '';
    if (r?.ok) return r;
    if (!r) S.err[name] = { '': `Can't reach ${S.host}. Check the connection and try again.` };
    else {
      if (r.status === 429) lock(r.data.retryAfter || 300);
      S.err[name] = fieldErrors(r.data, fields);
    }
    render();
    const f = form();
    if (!f) return r;
    const errs = S.err[name], secret = n => f.elements[n].hasAttribute('data-secret');
    fields.forEach(n => { if (!keepValue(n, secret(n), errs)) f.elements[n].value = ''; });
    const first = fields.find(n => errs[n]) || (errs[''] && fields.find(secret));
    f.elements[first]?.focus();
    return r;
  }

  // field is one labelled input of a sign-in form, with its hint or error.
  function field(form, name, label, type, hint, ac, value = '') {
    const id = `${form}-${name}`, e = S.err[form]?.[name], pw = type === 'password', show = pw && S.shown[id];
    const off = S.busy === form || locked() ? ' disabled' : '';
    const input = `<input class="inp" id="${id}" name="${name}" type="${show ? 'text' : type}" autocomplete="${ac}" autocapitalize="off" spellcheck="false"
      value="${esc(value)}" data-k="${id}"${pw ? ' data-secret' : ''} aria-describedby="${id}-h"${e ? ' aria-invalid="true"' : ''}${off}>`;
    return `<div class="af"><label for="${id}">${label}</label>
      ${pw ? `<div class="pw">${input}<button class="btn quiet" type="button" data-act="showpw" data-for="${id}" data-k="${id}-show" aria-pressed="${!!show}" aria-label="Show ${label.toLowerCase()}">Show</button></div>` : input}
      <p id="${id}-h" class="${e ? 'err' : 'hint'}"${e ? ' role="alert"' : ''}>${e ? esc(e) : hint}</p></div>`;
  }
  // formError is a sign-in form's error about the whole form, such as a lockout.
  const formError = form => S.err[form]?.[''] ? `<p class="err" role="alert">${esc(S.err[form][''])}</p>` : '';
  const formOff = form => S.busy === form || locked() ? ' disabled' : '';

  function renderAuth() {
    const el = $('.auth');
    if (S.auth === 'boot') {
      el.innerHTML = S.conn === 'down'
        ? `<div class="auth-in">${banner('auth')}</div>`
        : `<div class="state" role="status"><b>Loading the panel from ${esc(S.host)}…</b><span>The first load takes a few seconds over a slow link.</span></div>`;
      return;
    }
    if (S.auth === 'setup') {
      el.innerHTML = `<div class="auth-in"><p class="brand">thefeed <span>admin</span></p>
        <h2>Create the admin account</h2>
        <p class="lead">First run on ${esc(S.host)}. This account is the only way into the panel, so keep the password somewhere safe.</p>
        <form data-form="setup" novalidate>
          ${field('setup', 'code', 'Setup code', 'text', 'The installer printed it. Only someone with access to the server has it, so nobody else can claim this panel.', 'one-time-code')}
          ${field('setup', 'username', 'Username', 'text', '3 to 32 letters, digits, dots, dashes or underscores', 'username', 'admin')}
          ${field('setup', 'password', 'Password', 'password', 'At least 12 characters', 'new-password')}
          ${field('setup', 'repeat', 'Password again', 'password', '', 'new-password')}
          ${formError('setup')}
          <button class="btn full" type="submit" data-k="setup-go"${formOff('setup')}>${S.busy === 'setup' ? 'Creating the account…' : 'Create admin and sign in'}</button>
        </form></div>`;
      return;
    }
    el.innerHTML = `<div class="auth-in"><p class="brand">thefeed <span>admin</span></p>
      <h2>Log in</h2><p class="lead">${esc(S.host)}</p>
      ${S.authMsg ? `<p class="pmsg" role="status">${esc(S.authMsg)}</p>` : ''}
      <form data-form="login" novalidate>
        ${field('login', 'username', 'Username', 'text', '', 'username')}
        ${field('login', 'password', 'Password', 'password', '', 'current-password')}
        ${formError('login')}
        <button class="btn full" type="submit" data-k="login-go"${formOff('login')}>${S.busy === 'login' ? 'Logging in…' : 'Log in'}</button>
      </form>
      <p class="hint">Forgot the password? Reset it with this on the server: <code>${esc(S.reset)}</code></p></div>`;
  }

  forms.setup = async () => {
    const r = await submit('setup', '/api/auth/setup', ['code', 'username', 'password', 'repeat']);
    if (r?.ok) return signIn(r.data, `Admin account “${r.data.username}” created. You're signed in.`);
    if (r?.status === 409) {
      Object.assign(S, { auth: 'login', authMsg: r.data.errors[0].message });
      render();
    }
  };
  forms.login = async () => {
    const r = await submit('login', '/api/auth/login', ['username', 'password']);
    if (r?.ok) signIn(r.data);
  };
  acts.showpw = b => {
    const show = !S.shown[b.dataset.for];
    S.shown[b.dataset.for] = show;
    $('#' + b.dataset.for).type = show ? 'text' : 'password';
    b.setAttribute('aria-pressed', String(show));
  };
  acts.logout = async () => {
    const r = await api('POST', '/api/auth/logout', {});
    if (r?.ok) signOut("You've logged out.");
  };

  // ---------- shared states ----------
  function banner(where) {
    if (S.conn !== 'down') return '';
    return `<div class="banner" role="alert"><b>Can't reach ${esc(S.host)} since ${S.downAt}.</b>
      <div class="row2"><span>${esc(S.retryMsg || "You're seeing what it reported then.")}</span>
      <button class="btn quiet" type="button" data-act="retry" data-k="retry-${where}"${S.retrying ? ' disabled' : ''}>${S.retrying ? 'Trying…' : 'Try again'}</button></div></div>`;
  }
  const extra = where => banner(where) + banners.map(b => b(where)).join('');
  const loading = what => `<div class="state" role="status"><b>Loading ${what} from ${esc(S.host)}…</b><span>The first load takes a few seconds over a slow link.</span></div>`;
  function liveButton(first) {
    if (S.conn === 'down') return '<button class="livebtn" type="button" disabled>Offline</button>';
    if (first) return '<button class="livebtn" type="button" disabled>Loading</button>';
    return S.paused
      ? `<button class="livebtn" type="button" data-act="pause" data-k="pause" aria-label="Resume live updates">${I.play}Paused</button>`
      : '<button class="livebtn" type="button" data-act="pause" data-k="pause" aria-label="Pause live updates"><span class="dot" aria-hidden="true"></span>Live</button>';
  }
  const head = (title, where, live, first) => `<div class="bar"><div class="titles"><h2>${title}</h2><div class="sub">${esc(S.host)}</div></div>${live ? liveButton(first) : ''}</div>${extra(where)}`;
  const detailHead = (view, title, sub, back, below = extra('section')) => `<div class="bar"><button class="back" type="button" data-act="close" data-k="back-${view}" aria-label="${back}">${I.back}</button>
    <div class="titles"><h2>${esc(title)}</h2><div class="sub">${esc(sub)}</div></div></div>${below}`;
  const sec = (title, inner, note = '') => `<section class="cg"><h3>${title}</h3>${note ? `<p class="note">${note}</p>` : ''}<div class="card">${inner}</div></section>`;
  const summary = s => { const r = s.summary?.() ?? ''; return Array.isArray(r) ? r : [r]; };
  const waits = s => { const n = s.wait?.() || 0; return n ? `<small class="wait">${plural(n, 'change waits', 'changes wait')} for a restart</small>` : ''; };

  // ---------- the app ----------
  function renderNav() {
    const n = (S.conn === 'live' && views.board.count?.()) || 0;
    $('.nav').innerHTML = NAV.map(([v, label]) => `<button type="button" data-act="view" data-v="${v}" data-k="nav-${v}"${S.view === v ? ' aria-current="page"' : ''}>${I[v]}<span>${label}</span>${v === 'board' && n
      ? `<span class="count" aria-hidden="true">${n}</span><span class="sr">, ${plural(n, 'feature needs', 'features need')} attention</span>` : ''}</button>`).join('');
  }
  function renderBoard() {
    const v = views.board, body = v.render();
    const flash = S.flash ? `<div class="notice" role="status"><span>${esc(S.flash)}</span></div>` : '';
    $('.v-board .list-pane .scroll').innerHTML = head('Board', 'board', true, body == null) + flash + (body ?? loading(v.loading));
    const d = S.open.board != null && v.detail(S.open.board);
    $('.v-board .thread-pane .scroll').innerHTML = d ? detailHead('board', d.title, d.sub, 'Back to the board', banner('feature')) + d.html
      : body == null ? '' : '<div class="state"><b>Pick a feature</b><span>Its status, uptime, dependencies, incidents and log open here.</span></div>';
  }
  function renderServer() {
    const v = views.server, body = v.render();
    $('.v-server .scroll').innerHTML = head('Server', 'server', true, body == null) + (body ?? loading(v.loading));
  }
  function renderControl() {
    $('.v-control .list-pane .scroll').innerHTML = head('Control', 'control') + groupSections(sections, GROUPS).map(([g, ss]) => sec(g, ss.map(s => {
      const [text, cls] = summary(s);
      return `<button class="cl" type="button" data-act="open" data-id="${s.id}" data-k="row-${s.id}"${S.open.control === s.id ? ' aria-current="true"' : ''}><span class="tx"><b>${esc(s.title)}</b><small${cls ? ` class="${cls}"` : ''}>${esc(text)}</small>${waits(s)}</span><span class="chev" aria-hidden="true"></span></button>`;
    }).join(''))).join('');
    const s = sections.find(s => s.id === S.open.control);
    $('.v-control .thread-pane .scroll').innerHTML = s
      ? detailHead('control', s.title, summary(s)[0], 'Back to Control') + `<p class="pmsg" role="status" tabindex="-1" data-k="pmsg">${esc(S.msg[s.id] || '')}</p><div class="cdet">${s.render()}</div>`
      : '<div class="state"><b>Pick a section</b><span>Power controls are under Service and power, channel and account lists under Sources. Changes that need a restart wait until you restart.</span></div>';
  }

  // render draws everything from S. Focus stays on the element with the same
  // data-k, and a typed value stays unless the page now renders a new one.
  function render() {
    const ae = document.activeElement, k = root.contains(ae) ? ae.closest('[data-k]')?.dataset.k : null;
    const typed = new Map([...root.querySelectorAll('input[data-k], textarea[data-k]')].map(el => [el.dataset.k, [el.defaultValue, el.value]]));
    root.dataset.theme = S.theme;
    const app = S.auth === 'in';
    $('.app-in').hidden = !app;
    $('.auth').hidden = app;
    if (app) {
      $('.auth').innerHTML = '';
      root.classList.toggle('has-thread', !!S.open[S.view]);
      NAV.forEach(([v]) => { $(`.v-${v}`).hidden = S.view !== v; });
      renderNav(); renderBoard(); renderServer(); renderControl();
    } else {
      // Nothing typed in the app outlives signing out, passwords included.
      root.querySelectorAll('.nav, .app-in .scroll').forEach(el => { el.innerHTML = ''; });
      renderAuth();
    }
    for (const el of root.querySelectorAll('input[data-k], textarea[data-k]')) {
      const t = typed.get(el.dataset.k);
      if (t && t[0] === el.defaultValue) el.value = t[1];
    }
    const el = k && visible(`[data-k="${CSS.escape(k)}"]`);
    if (el && el !== document.activeElement) {
      el.focus();
      if (/^(text|search|password)$/.test(el.type)) el.setSelectionRange(el.value.length, el.value.length);
    }
  }

  // Opening a row shows its page; on a phone it takes the screen and focus
  // goes to Back. Closing returns focus to the row.
  function openRow(id) {
    const v = S.view;
    S.open[v] = id;
    S.flash = '';
    S.msg = {};
    if (v === 'control') sections.find(s => s.id === id)?.open?.();
    else views[v]?.open?.(id);
    render();
    const bk = visible(`.v-${v} .back`);
    if (bk) bk.focus();
  }
  function closeRow() {
    const v = S.view, id = S.open[v];
    S.open[v] = null;
    render();
    $(`.v-${v} [data-k="row-${CSS.escape(id)}"]`)?.focus();
  }
  acts.view = b => { S.view = b.dataset.v; S.flash = ''; render(); };
  acts.open = b => openRow(b.dataset.id);
  acts.close = closeRow;
  acts.pause = () => {
    S.paused = !S.paused;
    if (!S.paused) polls.forEach(p => { p.due = 0; });
    render();
  };
  acts.retry = reconnect;

  // A redraw between press and release would replace the pressed button and
  // lose the click: a field that commits on change blurs first, and a poll
  // can land too. So Panel.render waits for the release; the click lands,
  // then it draws. Only a press on a control holds: a scrollbar drag may
  // never send its release.
  let down = false, held = false;
  root.addEventListener('pointerdown', e => { down = !!e.target.closest('button, input, textarea, select, label, [data-act]'); });
  const release = () => { down = false; if (held) { held = false; setTimeout(render); } };
  ['pointerup', 'pointercancel', 'blur'].forEach(t => addEventListener(t, release));
  const draw = () => { if (down) held = true; else render(); };

  root.addEventListener('click', e => {
    const b = e.target.closest('[data-act]');
    if (b && !b.disabled) acts[b.dataset.act]?.(b, e);
  });
  root.addEventListener('submit', e => {
    e.preventDefault();
    forms[e.target.dataset.form]?.(e.target, e);
  });
  root.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || S.auth !== 'in' || !S.open[S.view]) return;
    const page = S.view === 'control' ? sections.find(s => s.id === S.open.control) : views[S.view];
    if (page?.escape?.()) return;
    if (e.target.matches('input, textarea, select')) return;
    closeRow();
  });
  // ---------- dividers ----------
  // A divider drags, takes arrow keys (Shift for bigger steps) or Home and
  // End, and resets on a double-click. The sidebar's sets --nav-w; the
  // list's sets --list-w, one width for the Board and Control. Each keeps
  // to the limits the CSS clamps it to, and is kept in this browser.
  const SPLITS = {
    nav: { min: 160, max: () => 320, pane: () => $('.nav') },
    list: { min: 260, max: split => split.closest('.view').clientWidth - 320, pane: split => split.parentElement },
  };
  const widthKey = name => `thefeed-admin.${name}-w`;
  function keepWidth(name) {
    try {
      const w = root.style.getPropertyValue(`--${name}-w`);
      if (w) localStorage.setItem(widthKey(name), w); else localStorage.removeItem(widthKey(name));
    } catch {}
  }
  for (const split of root.querySelectorAll('.split')) {
    const name = split.dataset.split, d = SPLITS[name], pane = () => d.pane(split).getBoundingClientRect();
    try { const w = localStorage.getItem(widthKey(name)); if (w) root.style.setProperty(`--${name}-w`, w); } catch {}
    // value says where the divider is and how far it goes; set moves it.
    const value = px => {
      const max = Math.max(d.min, d.max(split));
      px = Math.round(Math.max(d.min, Math.min(px, max)));
      split.setAttribute('aria-valuemin', d.min);
      split.setAttribute('aria-valuemax', max);
      split.setAttribute('aria-valuenow', px);
      return px;
    };
    const set = px => root.style.setProperty(`--${name}-w`, value(px) + 'px');
    split.addEventListener('focus', () => value(pane().width));
    split.addEventListener('pointerdown', e => {
      if (e.button !== 0) return;
      split.setPointerCapture(e.pointerId);
      split.classList.add('drag');
      root.classList.add('resizing');
    });
    split.addEventListener('pointermove', e => { if (split.hasPointerCapture(e.pointerId)) set(e.clientX - pane().left); });
    split.addEventListener('lostpointercapture', () => {
      split.classList.remove('drag');
      root.classList.remove('resizing');
      keepWidth(name);
    });
    split.addEventListener('keydown', e => {
      const step = e.shiftKey ? 80 : 20, now = pane().width;
      const to = { ArrowLeft: now - step, ArrowRight: now + step, Home: 0, End: Infinity }[e.key];
      if (to === undefined) return;
      e.preventDefault();
      set(to);
      keepWidth(name);
    });
    split.addEventListener('dblclick', () => {
      root.style.removeProperty(`--${name}-w`);
      keepWidth(name);
      value(pane().width);
    });
  }

  document.addEventListener('DOMContentLoaded', boot);

  return {
    S, I, acts, forms, banners, api, render: draw, submit, field, formError, formOff, sec, loading,
    view(id, page) { views[id] = page; },
    section(s) { sections.push(s); },
    poll(fn, ms = 5000) { polls.push({ fn, ms, due: 0 }); },
    // say puts a message at the top of the open Control section.
    say(text) { S.msg[S.open.control] = text; },
  };
})();
