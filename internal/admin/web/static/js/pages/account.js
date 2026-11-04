// Control, This panel, Account: who is signed in and since when, Log out,
// Change password, and every session signed in now.
'use strict';
(() => {
  const S = Panel.S;
  let sessions = null; // null while loading, false if it couldn't load

  async function load() {
    sessions = null;
    const r = await Panel.api('GET', '/api/account/sessions');
    sessions = r?.ok ? r.data.sessions : false;
    Panel.render();
  }

  function sessionRows() {
    if (sessions === null) return '<div class="it"><div class="tx"><small>Loading sessions…</small></div></div>';
    if (sessions === false) return '<div class="it"><div class="tx"><small class="bad">Couldn\'t load the sessions. Open Account again to retry.</small></div></div>';
    return sessions.map(s => `<div class="it"><div class="tx"><b>${esc(clientLabel(s.client))}</b>
      <small>${s.current ? `This device. Signed in ${when(s.since)}.` : `Signed in ${when(s.since)}. Last used ${when(s.lastSeen)}.`}</small></div></div>`).join('');
  }

  Panel.section({
    group: 'This panel', id: 'acct', title: 'Account',
    summary: () => `${S.me.username}, signed in since ${when(S.me.since)}`,
    open: load,
    render: () => Panel.sec('Signed in', `<div class="it"><div class="tx"><b>${esc(S.me.username)}</b><small>Since ${when(S.me.since)} on this device. A session ends after 8 hours without use.</small></div>
          <button class="btn quiet" type="button" data-act="logout" data-k="logout">Log out</button></div>`,
        "The panel keeps this account in its own database on the server and stores the password only as a hash. There's one admin, and no other way in.")
      + Panel.sec('Change password', `<form class="addf chpw" data-form="pw" novalidate>
          ${Panel.field('pw', 'current', 'Current password', 'password', '', 'current-password')}
          ${Panel.field('pw', 'password', 'New password', 'password', 'At least 12 characters', 'new-password')}
          ${Panel.field('pw', 'repeat', 'New password again', 'password', '', 'new-password')}
          ${Panel.formError('pw')}
          <div class="acts"><button class="btn" type="submit" data-k="pw-go"${Panel.formOff('pw')}>${S.busy === 'pw' ? 'Changing the password…' : 'Change password'}</button></div></form>`,
        'Changing it logs out every other session.')
      + Panel.sec('Sessions', sessionRows(), 'Every browser signed in now. Each one ends after 8 hours without use, or 7 days after signing in.'),
  });

  Panel.forms.pw = async () => {
    const r = await Panel.submit('pw', '/api/auth/password', ['current', 'password', 'repeat']);
    if (!r?.ok) return;
    S.shown = {};
    Panel.say('Password changed. Every other session was logged out.');
    Panel.render();
    document.querySelector('form[data-form="pw"]').reset();
    document.querySelector('.v-control [data-k="pmsg"]').focus();
    load();
  };
})();
