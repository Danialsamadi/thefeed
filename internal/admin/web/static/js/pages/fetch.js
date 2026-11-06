// Control, Server settings, Fetching: how often channels are fetched, how
// many posts each keeps, and the Telegram login with Log out, which asks
// for the password and restarts the server. Everything applies after a
// restart.
'use strict';
(() => {
  const C = Ctl.C;
  const STEPS = { 'fetch.every': [3, 5, 10, 15, 20, 30, 60], 'fetch.msgs': [5, 10, 15, 20, 25, 30, 35, 40, 45, 50] };
  let confirm = false; // the Log out confirm replaces the login row
  let busy = false;    // logging out, then restarting
  let pwErr = '';      // why the password was turned down

  function step(key, label, help, unit, fewer, more) {
    const v = +Ctl.val(key), id = Ctl.fid(key), ok = d => stepTo(STEPS[key], v, d) != null && !Ctl.off();
    return `<div class="it"><div class="tx"><b id="${id}">${label}</b><small>${Ctl.after(help, key)}</small></div>
      <div class="step" role="group" aria-labelledby="${id}"><button type="button" data-act="c-step" data-key="${key}" data-d="-1" data-k="${key}-minus" aria-label="${fewer}"${ok(-1) ? '' : ' disabled'}>−</button><output aria-live="polite">${v}${unit}</output><button type="button" data-act="c-step" data-key="${key}" data-d="1" data-k="${key}-plus" aria-label="${more}"${ok(1) ? '' : ' disabled'}>+</button></div></div>`;
  }

  function login() {
    const t = C.st.telegram;
    if (busy) return '<div class="it"><div class="tx"><b>Telegram login</b><small>Logging out, then restarting…</small></div></div>';
    if (confirm) {
      return `<form class="it confirm danger" data-form="c-login-go" novalidate><div class="tx"><b>Log out of Telegram?</b><small>The server restarts: readers lose about 10 seconds of queries. Private channels stop updating; public channels keep working without a login. Logging in again needs SSH to the server.</small>
          ${pwField('login-pw', pwErr)}</div>
        <div class="acts"><button class="btn quiet" type="button" data-act="c-login-cancel" data-k="login-cancel">Cancel</button><button class="btn danger" type="submit" data-k="login-go">Log out</button></div></form>`;
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
    [confirm, pwErr] = [true, ''];
    Ctl.render();
    Ctl.focus('login-pw');
  };
  Panel.acts['c-login-cancel'] = cancel;
  Panel.forms['c-login-go'] = async f => {
    [confirm, busy] = [false, true];
    Ctl.render();
    const r = await Ctl.call('POST', '/api/settings/telegram-logout', { password: f.elements.password.value });
    if (pwRefused(r)) {
      [confirm, busy, pwErr] = [true, false, Ctl.error(r)];
      Ctl.render();
      return Ctl.focus('login-pw');
    }
    // The restart is Service and power's, which works in either mode.
    const re = r?.ok && await Panel.api('POST', '/api/service/restart', {});
    busy = false;
    if (r) {
      Panel.say(!r.ok ? Ctl.error(r) : re?.ok ? 'Logged out of Telegram and restarted. Private channels stopped; public channels keep working.'
        : `Logged out of Telegram, but the restart failed${re ? `: ${Ctl.error(re)}` : ''}. Private channels stop after the next restart.`);
    }
    Ctl.render();
    Ctl.focus('pmsg');
  };
})();
