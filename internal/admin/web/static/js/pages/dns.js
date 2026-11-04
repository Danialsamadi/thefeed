// Control, Server settings, DNS and domains: the domains the server answers
// for, with an NS check against public resolvers, and how it answers.
'use strict';
(() => {
  const S = Panel.S, C = Ctl.C;
  const SERVES = { chat: 'Messenger', mtproto: 'Telegram over DNS' };
  let ns = null; // 'checking', or POST /api/domains/check's answer

  function domains() {
    const done = ns && ns !== 'checking', list = done ? ns.domains : C.domains || [];
    const line = ns === 'checking' ? 'Asking public resolvers…' : done ? nsLine(list, ns.checkedAt) : 'NS records not checked yet.';
    const table = list.length ? `<div class="pad"><table class="tbl"><thead><tr><th scope="col">Domain</th><th scope="col">Used for</th><th scope="col">NS record</th></tr></thead>
      <tbody>${list.map(d => {
        const [text, cls] = done ? nsCell(d) : [ns ? 'Checking' : 'Not checked', 'sw-pending'];
        return `<tr><td>${esc(d.domain)}</td><td>${SERVES[d.serves] || (d.main ? 'Feed, main' : 'Feed')}</td><td class="${cls}">${esc(text)}</td></tr>`;
      }).join('')}</tbody></table></div>` : '<p class="empty pad">No domains are set in thefeed.env.</p>';
    return `<div class="tools"><span role="status">${esc(line)}</span>
      <button class="btn quiet" type="button" data-act="c-ns" data-k="nscheck"${ns === 'checking' || S.conn !== 'live' || !list.length ? ' disabled' : ''}>${ns === 'checking' ? 'Checking…' : 'Check NS records'}</button></div>${table}`;
  }

  Ctl.section({
    group: 'Server settings', id: 'dns', title: 'DNS and domains',
    render: () => Panel.sec('Domains', domains(), 'A new domain needs an NS record at your DNS host first, so adding one stays in the install script.')
      + Panel.sec('Answers', Ctl.num('dns.padding', 'Random padding', 'Added to each answer so sizes vary. 0 turns it off.', 'bytes')
        + Ctl.sw('dns.manage', 'Remote management', 'Lets readers with the key add and remove channels over DNS')
        + Ctl.sw('dns.debug', 'Log every query', 'For troubleshooting only. The log grows fast.')),
  });

  Panel.acts['c-ns'] = async () => {
    const was = ns;
    ns = 'checking';
    Ctl.render();
    const r = await Panel.api('POST', '/api/domains/check', {});
    ns = r?.ok ? r.data : was;
    if (r && !r.ok) Panel.say(Ctl.error(r));
    Ctl.render();
  };
})();
