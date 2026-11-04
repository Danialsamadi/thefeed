// Control, Sources, Telegram channels: add by name, link or invite link,
// search, and rename, reorder or remove. Public channels apply live; private
// ones (invite links) after a restart.
'use strict';
(() => {
  const S = Panel.S, C = Ctl.C;
  let fetching = false;

  Ctl.section({
    group: 'Sources', id: 'chan', title: 'Telegram channels',
    render() {
      const pub = C.st.lists.channels.items, pv = C.st.lists.private.items, q = C.q.trim().toLowerCase().replace(/^@/, '');
      const shown = q ? pub.filter(it => it.name.toLowerCase().includes(q)) : pub;
      const fetch = Panel.check ? `<button class="btn quiet" type="button" data-act="c-fetchall" data-k="fetchall"${fetching || S.conn !== 'live' ? ' disabled' : ''}>${fetching ? 'Fetching…' : 'Fetch all now'}</button>` : '';
      return Panel.sec('Add', Ctl.addForm('channels', 'Channel name or link', '@channel, t.me/channel or an invite link',
        'Public channels start fetching right away. Invite links go under Private channels and need a restart.'))
        + Panel.sec(`Public channels, ${pub.length}`, `<div class="tools"><span>Changes here apply right away.</span>${fetch}</div>
          <div class="filter"><label class="hint" for="c-q">Find a channel</label><input class="inp" id="c-q" type="search" autocomplete="off" autocapitalize="off" spellcheck="false" value="${esc(C.q)}" data-k="c-q"></div>
          ${Ctl.rows('channels', shown, q ? `No channel matches “${esc(C.q)}”. <button class="linkbtn" type="button" data-act="c-clearq" data-k="clearq">Clear the search</button>` : 'No public channels yet. Add one above.')}`)
        + Panel.sec(`Private channels, ${pv.length}`, Ctl.rows('private', pv, 'No private channels. Paste an invite link above to add one.'),
          C.st.telegram.loggedIn ? 'Joined with the Telegram login. Changes apply after a restart.'
            : `These need the Telegram login, which is logged out (see Fetching), so they ${Ctl.setting('fetch.noTelegram').waiting ? 'stop updating after a restart' : 'are not updating'}.`);
    },
  });

  // The search narrows the list as you type.
  document.getElementById('app').addEventListener('input', e => {
    if (e.target.dataset.k !== 'c-q') return;
    C.q = e.target.value;
    Ctl.render();
  });
  Panel.acts['c-clearq'] = () => {
    C.q = '';
    Ctl.render();
    Ctl.focus('c-q');
  };
  Panel.acts['c-fetchall'] = async () => {
    fetching = true;
    Ctl.render();
    const line = await Panel.check('chan');
    fetching = false;
    if (line) Panel.say(line);
    Ctl.render();
  };
})();
