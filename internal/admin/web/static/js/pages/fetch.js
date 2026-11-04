// Control, Server settings, Fetching: how often channels are fetched, how
// many posts each keeps, and the Telegram login with Log out. Everything
// applies after a restart.
'use strict';
(() => {
  const C = Ctl.C;
  const STEPS = { 'fetch.every': [3, 5, 10, 15, 20, 30, 60], 'fetch.msgs': [5, 10, 15, 20, 25, 30, 35, 40, 45, 50] };
  let confirm = false; // the Log out confirm replaces the login row

  function step(key, label, help, unit, fewer, more) {
    const v = +Ctl.val(key), id = Ctl.fid(key), ok = d => stepTo(STEPS[key], v, d) != null && !Ctl.off();
    return `<div class="it"><div class="tx"><b id="${id}">${label}</b><small>${Ctl.after(help, key)}</small></div>
      <div class="step" role="group" aria-labelledby="${id}"><button type="button" data-act="c-step" data-key="${key}" data-d="-1" data-k="${key}-minus" aria-label="${fewer}"${ok(-1) ? '' : ' disabled'}>−</button><output aria-live="polite">${v}${unit}</output><button type="button" data-act="c-step" data-key="${key}" data-d="1" data-k="${key}-plus" aria-label="${more}"${ok(1) ? '' : ' disabled'}>+</button></div></div>`;
  }

  function login() {
    const t = C.st.telegram;
    if (confirm) {
      return `<div class="it confirm danger"><div class="tx"><b>Log out of Telegram?</b><small>Private channels stop updating after the next restart. Public channels keep working without a login.</small></div>
        <div class="acts"><button class="btn quiet" type="button" data-act="c-login-cancel" data-k="login-cancel">Cancel</button><button class="btn danger" type="button" data-act="c-login-go" data-k="login-go">Log out</button></div></div>`;
    }
    if (!t.loggedIn) {
      return `<div class="it"><div class="tx"><b>Telegram login</b><small>${Ctl.after('Logged out', 'fetch.noTelegram')}. Public channels are read without it. To log in, run <code>thefeed-server --login-only</code> on the server.</small></div></div>`;
    }
    return `<div class="it"><div class="tx"><b>Telegram login</b><small>Logged in${t.sessionSaved ? `, session saved ${when(t.sessionSaved)}` : ''}. Private channels need it.</small></div>
      <button class="btn quiet" type="button" data-act="c-login-out" data-k="login-out"${Ctl.off()}>Log out…</button></div>`;
  }

  function cancel() {
    confirm = false;
    Ctl.render();
    Ctl.focus('login-out');
  }

  Ctl.section({
    group: 'Server settings', id: 'fetch', title: 'Fetching',
    open() { confirm = false; },
    escape() {
      if (!confirm) return false;
      cancel();
      return true;
    },
    render: () => Panel.sec('Telegram channels', step('fetch.every', 'Fetch channels every', 'Minimum 3 minutes', ' min', 'Fetch more often', 'Fetch less often')
      + step('fetch.msgs', 'Posts per channel', 'How many recent posts readers get', '', 'Fewer posts', 'More posts'))
      + Panel.sec('Login', login()),
  });

  Panel.acts['c-step'] = b => {
    const key = b.dataset.key, v = stepTo(STEPS[key], +Ctl.val(key), +b.dataset.d);
    if (v != null) Ctl.save(key, String(v));
  };
  Panel.acts['c-login-out'] = () => {
    confirm = true;
    Ctl.render();
    Ctl.focus('login-go');
  };
  Panel.acts['c-login-cancel'] = cancel;
  Panel.acts['c-login-go'] = async () => {
    const r = await Ctl.call('POST', '/api/settings/telegram-logout', {});
    if (!r) return;
    confirm = false;
    Panel.say(r.ok ? 'Logged out of Telegram. Private channels stop updating after a restart; public channels keep working.' : Ctl.error(r));
    Ctl.render();
    Ctl.focus('pmsg');
  };
})();
