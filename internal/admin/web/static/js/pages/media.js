// Control, Features, Media relays: the DNS relay and the GitHub relay, with
// the GitHub token replaced without ever being shown. Everything applies
// after a restart.
'use strict';
(() => {
  let form = false, err = '', saved = false; // the token form; its error; a new token saved this visit
  let pwErr = '';                             // why the password was turned down

  function token() {
    if (!form) {
      const set = Ctl.setting('media.ghToken').set;
      return `<div class="it"><div class="tx"><b>Token</b><small${saved ? ' class="wait"' : ''}>${saved ? 'New token saved, used after a restart' : set ? 'Saved on the server. It is never shown.' : 'Not set. The GitHub relay needs one.'}</small></div>
        <button class="btn quiet" type="button" data-act="c-token" data-k="token"${Ctl.off()}>${set ? 'Replace…' : 'Add…'}</button></div>`;
    }
    const repo = Ctl.val('media.ghRepo');
    return `<form class="addf" data-form="c-token" novalidate><label for="c-tok">New GitHub token</label>
      <input class="inp" id="c-tok" name="value" type="password" autocomplete="off" data-k="c-tok" aria-describedby="c-tok-h"${err ? ' aria-invalid="true"' : ''}>
      <p class="${err ? 'err' : 'hint'}" id="c-tok-h"${err ? ' role="alert"' : ''}>${err ? esc(err) : `Needs contents: write on ${repo ? esc(repo) : 'the repository'}. Stored on the server, never shown again.`}</p>
      ${pwField('c-tok-pw', pwErr)}
      <div class="acts"><button class="btn quiet" type="button" data-act="c-token-cancel" data-k="token-cancel">Cancel</button><button class="btn" type="submit" data-k="token-save"${Ctl.off()}>Save token</button></div></form>`;
  }

  function close() {
    [form, err, pwErr] = [false, '', ''];
    Ctl.render();
    Ctl.focus('token');
  }

  Ctl.section({
    group: 'Features', id: 'media', title: 'Media relays',
    open() { [form, err, pwErr] = [false, '', '']; },
    escape() {
      if (!form) return false;
      close();
      return true;
    },
    render: () => Panel.sec('DNS media relay', Ctl.sw('media.dnsOn', 'DNS media relay', 'Slow, but works wherever DNS does')
        + Ctl.num('media.dnsMaxKB', 'Largest file', '0 means no limit', 'KB')
        + Ctl.num('media.dnsTTLMin', 'Keep files cached for', '', 'min')
        + Ctl.opts('media.dnsComp', 'Compression', 'gzip suits most images and text'))
      + Panel.sec('GitHub media relay', Ctl.sw('media.ghOn', 'GitHub media relay', 'Fast, for larger files, through a GitHub repository')
        + Ctl.txt('media.ghRepo', 'Repository', 'owner/name')
        + Ctl.txt('media.ghBranch', 'Branch', 'Uploads are committed here')
        + Ctl.num('media.ghMaxKB', 'Largest file', '0 means no limit', 'KB')
        + Ctl.num('media.ghTTLMin', 'Keep files for', '', 'min')
        + Ctl.num('media.ghQuotaGB', 'Repository budget', 'You get a warning as it fills up', 'GB')
        + token()),
  });

  Panel.acts['c-token'] = () => {
    [form, err, pwErr] = [true, '', ''];
    Ctl.render();
    Ctl.focus('c-tok');
  };
  Panel.acts['c-token-cancel'] = close;
  Panel.forms['c-token'] = async f => {
    const r = await Ctl.call('POST', '/api/settings/github-token', { value: f.elements.value.value, password: f.elements.password.value });
    if (!r) return;
    if (!r.ok) {
      const pw = pwRefused(r);
      [err, pwErr] = pw ? ['', Ctl.error(r)] : [Ctl.error(r), ''];
      Ctl.render();
      return Ctl.focus(pw ? 'c-tok-pw' : 'c-tok');
    }
    saved = true;
    Panel.say('New token saved. The relay uses it after a restart.');
    close();
  };
})();
