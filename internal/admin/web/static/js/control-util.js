// Control's pure helpers: no DOM, so node tests load this file as is
// (jstest/control.test.mjs, run by go test). Uses plural from util.js.
'use strict';

// checkNumber is "" when raw is a whole number from min to max, else the
// message the server's registry gives (serverconfig.Validate).
function checkNumber(raw, min, max) {
  const v = String(raw).trim();
  if (/^-?\d+$/.test(v) && +v >= min && +v <= max) return '';
  return `Use a whole number from ${min.toLocaleString('en')} to ${max.toLocaleString('en')}.`;
}

// isOn reads a saved boolean the way the server does (serverconfig.ParseBool).
const isOn = v => /^(1|true|yes|on)$/i.test(String(v ?? '').trim());

// summary is a Control section's line under its title, from the saved
// values v (key → string), the API's lists and how many domains it serves.
function summary(id, v, lists, domains) {
  const n = l => lists[l].items.length;
  switch (id) {
    case 'chan': return `${n('channels')} public, ${n('private')} private`;
    case 'xacc': return plural(n('x'), 'account', 'accounts');
    case 'chat': return isOn(v['chat.on']) ? `On, ${v['chat.sendPerHour']} sends an hour per address` : 'Off';
    case 'mt': return isOn(v['mt.on']) ? `On, ${+v['mt.maxTunnels'] ? `up to ${v['mt.maxTunnels']}` : 'no limit on'} tunnels` : 'Off';
    case 'media': return `DNS relay ${isOn(v['media.dnsOn']) ? 'on' : 'off'}, GitHub relay ${isOn(v['media.ghOn']) ? 'on' : 'off'}`;
    case 'fetch': return `Every ${v['fetch.every']} min, ${v['fetch.msgs']} posts per channel`;
    case 'dns': return domains ? `${plural(domains, 'domain', 'domains')}, padding ${v['dns.padding']} bytes` : `Padding ${v['dns.padding']} bytes`;
  }
  return '';
}

// itemName is how a list entry shows. An invite link shows its first few
// characters only, so a screenshot doesn't leak it.
function itemName(list, name) {
  if (list === 'private') return name.replace(/^\w+:\/\//, '').slice(0, 10) + '…';
  return list === 'instances' ? name : '@' + name;
}

// listDone is what a list change says. res is POST /api/lists' answer (the
// list that changed, live, reloadError); name the entry as the server stored
// it; n its channel number, or its place for an instance.
function listDone(res, op, name, n) {
  const l = res.list, it = itemName(l, name);
  if (res.reloadError) return `Saved, but thefeed-server didn't reload the channels: ${res.reloadError}. They apply after a restart.`;
  if (l === 'channels') {
    return { add: `Added ${it} as channel ${n}. Fetching its posts now.`, rename: `Renamed to ${it}. Fetching its posts now.`,
      move: `Moved ${it} to channel ${n}. Readers get the new order on their next refresh.`,
      remove: `Removed ${it}. The channels after it moved up one number.` }[op];
  }
  if (op === 'add') {
    return { private: 'Added the invite link under Private channels. The server joins it after a restart.',
      x: `Added ${it}. It's fetched after a restart.`, instances: `Added ${it}. The server tries it after a restart, in the order shown.` }[l];
  }
  const what = { rename: `Renamed to ${it}`, move: `Moved ${it} to ${l === 'instances' ? 'place' : 'channel'} ${n}`, remove: `Removed ${it}` }[op];
  return `${what}. Applies after a restart.`;
}

// stepTo is the preset after v (d > 0) or before it (d < 0), or null at
// the end. A saved value between presets steps to its neighbours.
function stepTo(presets, v, d) {
  return (d > 0 ? presets.find(p => p > v) : presets.findLast(p => p < v)) ?? null;
}

// nsLine sums up an NS check of domains, done at checkedAt.
function nsLine(domains, checkedAt, now = new Date()) {
  const n = domains.length, k = domains.filter(d => d.pointsHere).length;
  const said = k < n ? `${k} of ${n} point` : n === 1 ? 'it points' : `all ${n} point`;
  return `Checked at ${when(checkedAt, now)}: ${said} to this server.`;
}

// nsCell is one domain's NS result: [text, class].
function nsCell(d) {
  if (d.pointsHere) return ['Points here', 'sw-ok'];
  return [d.ns?.length ? `Points to ${d.ns.join(', ')}` : 'No NS record found', 'sw-down'];
}

// waitingIn counts a section's changes that wait for a restart: its
// settings, plus the public and private channels on chan and the X accounts
// on xacc. The Nitter instances are a setting already; public channels wait
// only when the server didn't reload them.
function waitingIn(st, id) {
  const lists = { chan: ['channels', 'private'], xacc: ['x'] }[id] || [];
  return st.settings.filter(s => s.page === id && s.waiting).length + lists.filter(l => st.lists[l].waiting).length;
}
