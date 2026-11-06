// Control, Server, Alerts: what's firing now, the rules, and the places
// alerts go, as Shoutrrr URLs. internal/admin/alerts holds and sends them.
// Rule fields save on change; adding a URL asks for the password.
'use strict';
(() => {
  let st = null, failed = '';  // GET /api/alerts; why it couldn't load
  let err = {};                // field → error: "url", "password", "<rule>-above", "<rule>-minutes"
  let adding = false;          // the add form is open
  let busy = '';               // "add", "test", or a rule id while it saves
  let tested = null;           // the last test's results

  // id → [name, unit of its threshold, or '' for one without]
  const RULES = {
    server: ['thefeed-server stops or stops answering', ''],
    feature: ['A feature is down', ''],
    cpu: ['CPU busy', '%'],
    ram: ['RAM used', '%'],
    disk: ['Disk full', '%'],
    net: ['Outbound traffic', 'Mb/s'],
  };
  const min = n => `${n} ${n === 1 ? 'minute' : 'minutes'}`;
  const amount = (v, unit) => unit === '%' ? `${v}%` : `${v} ${unit}`;
  const why = r => ({
    server: `Sends after ${min(r.minutes)} stopped or silent, so a restart passes quietly. A stop from this panel counts too.`,
    feature: `Sends after ${min(r.minutes)} down (not slow). Waits while the server itself is down.`,
  }[r.id] || `Sends after ${min(r.minutes)} above ${amount(r.above, RULES[r.id][1])}.`);

  async function load() {
    const r = await Panel.api('GET', '/api/alerts');
    if (!r) return;
    if (r.ok) [st, failed] = [r.data, ''];
    else failed = r.data.errors?.[0]?.message || "Couldn't read the alerts.";
    Panel.render();
  }
  Panel.poll(load, 30000);

  function ruleRow(r) {
    const [name, unit] = RULES[r.id], id = `al-${r.id}`, off = busy ? ' disabled' : '';
    const num = (f, label, u, step) => {
      const e = err[`${r.id}-${f}`];
      return `<span class="numf"><input class="inp" type="number" inputmode="decimal" min="0" step="${step}" value="${esc(r[f])}" data-rule="${r.id}" data-f="${f}" data-k="${id}-${f}"
        aria-label="${esc(`${name}: ${label}`)}"${e ? ` aria-invalid="true" title="${esc(e)}"` : ''}${off}><span>${u}</span></span>`;
    };
    const e = err[`${r.id}-above`] || err[`${r.id}-minutes`];
    return `<div class="it"><div class="tx"><b id="${id}">${esc(name)}</b><small${e ? ' class="bad"' : ''}>${esc(e || why(r))}</small></div>
      <span class="alc">${unit ? num('above', 'above', unit, 'any') : ''}${num('minutes', 'minutes', 'min', 1)}
      <span class="onoff" aria-hidden="true">${r.on ? 'On' : 'Off'}</span>
      <button class="sw-btn" type="button" role="switch" aria-checked="${r.on}" aria-labelledby="${id}" data-act="al-sw" data-rule="${r.id}" data-k="${id}-sw"${off}><span class="tr"></span></button></span></div>`;
  }

  function urlRows() {
    const rows = st.urls.map((u, i) => {
      const t = tested?.find(x => x.url === u.url);
      return `<div class="it"><div class="tx"><b>${esc(u.url)}</b><small${t?.error ? ' class="bad"' : ''}>${t ? esc(t.error || 'The test arrived.') : 'Alerts go here.'}</small></div>
        <button class="btn quiet" type="button" data-act="al-rm" data-i="${i}" data-k="al-rm-${i}"${busy ? ' disabled' : ''}>Remove</button></div>`;
    }).join('');
    const form = adding
      ? `<form class="addf" data-form="al-add" novalidate><label for="al-url">Shoutrrr URL</label>
          <input class="inp" id="al-url" name="url" type="text" autocomplete="off" autocapitalize="off" spellcheck="false" data-k="al-url" aria-describedby="al-url-h"${err.url ? ' aria-invalid="true"' : ''}>
          <p class="${err.url ? 'err' : 'hint'}" id="al-url-h"${err.url ? ' role="alert"' : ''}>${err.url ? esc(err.url)
            : 'Such as <code>telegram://TOKEN@telegram?chats=@channel</code>, <code>ntfy://ntfy.sh/topic</code> or <code>discord://TOKEN@WEBHOOK_ID</code>. Every service is listed at shoutrrr.nickfedor.com.'}</p>
          ${pwField('al-pw', err.password)}
          <div class="acts"><button class="btn quiet" type="button" data-act="al-cancel" data-k="al-cancel">Cancel</button><button class="btn" type="submit" data-k="al-go"${busy ? ' disabled' : ''}>${busy === 'add' ? 'Adding…' : 'Add'}</button></div></form>`
      : `<div class="it"><div class="tx"><small>${st.urls.length ? 'Each alert goes to every URL here.' : 'Nowhere yet: alerts need a place to go.'}</small></div>
          <div class="acts">${st.urls.length ? `<button class="btn quiet" type="button" data-act="al-test" data-k="al-test"${busy ? ' disabled' : ''}>${busy === 'test' ? 'Sending…' : 'Send a test'}</button>` : ''}
          <button class="btn${st.urls.length ? ' quiet' : ''}" type="button" data-act="al-open" data-k="al-open"${busy ? ' disabled' : ''}>Add…</button></div></div>`;
    return rows + form;
  }

  function page() {
    if (!st) return failed ? `<div class="state" role="alert"><b>Couldn't read the alerts</b><span>${esc(failed)}</span></div>` : Panel.loading('the alerts');
    return (st.firing.length ? Panel.sec('Firing now', st.firing.map(f => `<div class="it"><div class="tx"><b class="bad">${esc(f.text)}</b><small>Since ${when(f.since)}</small></div></div>`).join('')) : '')
      + Panel.sec('Send alerts to', urlRows(), 'Telegram, Discord, Slack, ntfy, Gotify, Pushover, Matrix, email and more, through Shoutrrr. A URL holds its token, so only its start is shown.')
      + Panel.sec('Rules', st.rules.map(ruleRow).join(''), `Checked every minute on ${esc(st.where)}. If this machine or the panel is down, nothing is sent.`);
  }

  Panel.section({
    group: 'Server', id: 'alerts', title: 'Alerts',
    summary() {
      if (!st) return '';
      if (st.firing.length) return [`${plural(st.firing.length, 'alert', 'alerts')} firing`, 'bad'];
      if (!st.urls.length) return ['Nowhere to send alerts', 'bad'];
      return `${st.rules.filter(r => r.on).length} of ${st.rules.length} rules on, ${plural(st.urls.length, 'place', 'places')} to send to`;
    },
    open() { [err, adding, tested] = [{}, false, null]; load(); },
    render: page,
    escape() {
      if (!adding) return false;
      close();
      return true;
    },
  });

  const focus = k => document.querySelector(`.v-control [data-k="${k}"]`)?.focus();
  function close() {
    [adding, err] = [false, {}];
    Panel.render();
    focus('al-open');
  }

  // A rule's fields and switch save the whole rule.
  async function saveRule(id, change) {
    const r = { ...st.rules.find(x => x.id === id), ...change };
    busy = id;
    Panel.render();
    const res = await Panel.api('POST', `/api/alerts/rules/${id}`, { on: r.on, above: +r.above, minutes: +r.minutes });
    busy = '';
    delete err[`${id}-above`];
    delete err[`${id}-minutes`];
    if (res?.ok) st = res.data;
    else if (res) {
      const e = res.data.errors?.[0] || {};
      err[`${id}-${e.field === 'above' ? 'above' : 'minutes'}`] = e.message || "Couldn't save the rule.";
    }
    Panel.render();
  }
  Panel.acts['al-sw'] = b => saveRule(b.dataset.rule, { on: !st.rules.find(x => x.id === b.dataset.rule).on });
  document.getElementById('app').addEventListener('change', e => {
    const t = e.target;
    if (!t.dataset.rule || !st) return;
    saveRule(t.dataset.rule, { [t.dataset.f]: t.value });
  });

  Panel.acts['al-open'] = () => {
    [adding, err] = [true, {}];
    Panel.render();
    focus('al-url');
  };
  Panel.acts['al-cancel'] = close;
  Panel.forms['al-add'] = async f => {
    busy = 'add';
    Panel.render();
    const r = await Panel.api('POST', '/api/alerts/urls', { url: f.elements.url.value, password: f.elements.password.value });
    busy = '';
    if (r?.ok) {
      [st, tested] = [r.data, null];
      Panel.say('Added. Send a test to check that alerts arrive.');
      return close();
    }
    if (r) {
      const msg = r.data.errors?.[0]?.message || "Couldn't add the URL.";
      err = pwRefused(r) ? { password: msg } : { url: msg };
    }
    Panel.render();
    if (err.password) {
      const pw = document.querySelector('.v-control [data-k="al-pw"]');
      if (pw) pw.value = '';
    }
    focus(err.password ? 'al-pw' : 'al-url');
  };
  Panel.acts['al-rm'] = async b => {
    busy = 'rm';
    Panel.render();
    const r = await Panel.api('POST', '/api/alerts/urls/remove', { index: +b.dataset.i });
    busy = '';
    if (r?.ok) [st, tested] = [r.data, null];
    Panel.say(r?.ok ? 'Removed. Alerts no longer go there.' : r?.data.errors?.[0]?.message || "Couldn't remove it.");
    Panel.render();
    focus('pmsg');
  };
  Panel.acts['al-test'] = async () => {
    busy = 'test';
    Panel.render();
    const r = await Panel.api('POST', '/api/alerts/test', {});
    busy = '';
    if (r?.ok) {
      tested = r.data.results;
      const bad = tested.filter(x => x.error).length;
      Panel.say(bad ? `${plural(bad, 'URL', 'URLs')} didn't take the test. The reason is under each.` : 'Sent a test to every URL. Check that it arrived.');
    } else if (r) Panel.say(r.data.errors?.[0]?.message || "Couldn't send the test.");
    Panel.render();
    focus('pmsg');
  };
})();
