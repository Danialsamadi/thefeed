// Unit tests for Control's pure helpers (static/js/control-util.js). Zero
// dependencies; run with node internal/admin/web/jstest/control.test.mjs, or
// by go test ./internal/admin/web (TestJSUtil).
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import vm from 'node:vm';

const js = join(dirname(fileURLToPath(import.meta.url)), '..', 'static', 'js');
for (const f of ['util.js', 'control-util.js']) vm.runInThisContext(readFileSync(join(js, f), 'utf8'), { filename: f });

let failed = 0, passed = 0;
function check(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { passed++; return; }
  failed++;
  console.error(`FAIL ${name}\n  got:  ${g}\n  want: ${w}`);
}

// ===== checkNumber: a number field commits only what the server takes, worded as the registry words it =====
check('in range', checkNumber('45', 1, 65535), '');
check('at the bounds', [checkNumber('0', 0, 255), checkNumber('255', 0, 255)], ['', '']);
check('over', checkNumber('256', 0, 255), 'Use a whole number from 0 to 255.');
check('big range has commas', checkNumber('-1', 0, 100000000), 'Use a whole number from 0 to 100,000,000.');
check('empty', checkNumber('', 1, 1440), 'Use a whole number from 1 to 1,440.');
check('not whole', checkNumber('2.5', 1, 60), 'Use a whole number from 1 to 60.');
check('exponent the server would refuse', checkNumber('1e3', 0, 102400), 'Use a whole number from 0 to 102,400.');
check('spaces are fine', checkNumber(' 30 ', 1, 65535), '');

// ===== summary: the line under each section's title, from saved values as the API sends them (strings) =====
const v = {
  'chat.on': 'true', 'chat.sendPerHour': '30', 'mt.on': 'yes', 'mt.maxTunnels': '50',
  'media.dnsOn': '1', 'media.ghOn': 'false', 'fetch.every': '10', 'fetch.msgs': '15', 'dns.padding': '32',
};
const lists = { channels: { items: Array(40).fill({}) }, private: { items: [{}, {}] }, x: { items: Array(6).fill({}) }, instances: { items: [{}] } };
check('channels', summary('chan', v, lists), '40 public, 2 private');
check('x accounts', summary('xacc', v, lists), '6 accounts');
check('one x account', summary('xacc', v, { ...lists, x: { items: [{}] } }), '1 account');
check('messenger on', summary('chat', v, lists), 'On, 30 sends an hour per address');
check('messenger off', summary('chat', { ...v, 'chat.on': 'off' }, lists), 'Off');
check('tunnels capped', summary('mt', v, lists), 'On, up to 50 tunnels');
check('tunnels uncapped', summary('mt', { ...v, 'mt.maxTunnels': '0' }, lists), 'On, no limit on tunnels');
check('tunnels off', summary('mt', { ...v, 'mt.on': '0' }, lists), 'Off');
check('media relays', summary('media', v, lists), 'DNS relay on, GitHub relay off');
check('fetching', summary('fetch', v, lists), 'Every 10 min, 15 posts per channel');
check('dns', summary('dns', v, lists, 5), '5 domains, padding 32 bytes');
check('dns before the domains load', summary('dns', v, lists), 'Padding 32 bytes');

// ===== waitingIn: how many changes on a section wait for a restart =====
// Shaped like GET /api/settings: the instances are a setting (xacc.instances)
// and a list, and count once; public channels apply live and never wait.
const st = {
  waiting: 5,
  settings: [
    { key: 'chat.on', page: 'chat', waiting: true }, { key: 'chat.inboxCap', page: 'chat', waiting: false },
    { key: 'xacc.instances', page: 'xacc', waiting: true }, { key: 'fetch.noTelegram', page: 'fetch', waiting: true },
    { key: 'media.ghToken', page: 'media', secret: true, waiting: false }, { key: 'server.listen', page: '', waiting: false },
  ],
  lists: { channels: { waiting: false }, private: { waiting: true }, x: { waiting: true }, instances: { waiting: true } },
};
const ids = ['chan', 'xacc', 'chat', 'mt', 'media', 'fetch', 'dns'];
check('per section', ids.map(id => waitingIn(st, id)), [1, 2, 1, 0, 0, 1, 0]);
check('adds up to the total', ids.reduce((n, id) => n + waitingIn(st, id), 0), st.waiting);
check('a section without settings', waitingIn(st, 'acct'), 0);

// ===== itemName: how a list entry is named on screen; invite links never in full =====
check('invite as the server stores it', itemName('private', 'https://t.me/+Xk2LqP0aZ7c4Hn'), 't.me/+Xk2L…');
check('joinchat invite', itemName('private', 'https://t.me/joinchat/Rt9PwM3vB8sQ1e'), 't.me/joinc…');
check('public channel', itemName('channels', 'daily_brief'), '@daily_brief');
check('x account', itemName('x', 'space_daily'), '@space_daily');
check('nitter instance keeps its scheme', itemName('instances', 'http://nitter.net'), 'http://nitter.net');

// ===== listDone: what a list change says, from POST /api/lists/{list}'s answer =====
// name is the entry as the server stored it; n its channel number, or its place for an instance.
const live = { list: 'channels', live: true };
check('add a public channel', listDone(live, 'add', 'daily_brief', 41), 'Added @daily_brief as channel 41. Fetching its posts now.');
check('an invite link pasted as a channel', listDone({ list: 'private', live: false }, 'add', 'https://t.me/+Xk2LqP0aZ7c4Hn', 43),
  'Added the invite link under Private channels. The server joins it after a restart.');
check('add an x account', listDone({ list: 'x' }, 'add', 'tech_wire', 49), "Added @tech_wire. It's fetched after a restart.");
check('add an instance', listDone({ list: 'instances' }, 'add', 'https://xcancel.com', 2), 'Added https://xcancel.com. The server tries it after a restart, in the order shown.');
check('rename a public channel', listDone(live, 'rename', 'world_desk', 2), 'Renamed to @world_desk. Fetching its posts now.');
check('rename an x account', listDone({ list: 'x' }, 'rename', 'tech_wire', 44), 'Renamed to @tech_wire. Applies after a restart.');
check('move a public channel', listDone(live, 'move', 'city_radio', 3), 'Moved @city_radio to channel 3. Readers get the new order on their next refresh.');
check('move an x account', listDone({ list: 'x' }, 'move', 'tech_wire', 43), 'Moved @tech_wire to channel 43. Applies after a restart.');
check('move an instance', listDone({ list: 'instances' }, 'move', 'https://xcancel.com', 1), 'Moved https://xcancel.com to place 1. Applies after a restart.');
check('remove a public channel', listDone(live, 'remove', 'film_club', 9), 'Removed @film_club. The channels after it moved up one number.');
check('remove a private channel', listDone({ list: 'private' }, 'remove', 'https://t.me/+Rt9PwM3vB8sQ1e', 42), 'Removed t.me/+Rt9P…. Applies after a restart.');
check('remove an instance', listDone({ list: 'instances' }, 'remove', 'http://nitter.net', 2), 'Removed http://nitter.net. Applies after a restart.');
check('saved but not reloaded', listDone({ list: 'channels', live: false, reloadError: 'dial unix control.sock: connect: no such file or directory' }, 'add', 'daily_brief', 41),
  "Saved, but thefeed-server didn't reload the channels: dial unix control.sock: connect: no such file or directory. They apply after a restart.");

// ===== stepTo: the fetch steppers go preset to preset, from wherever the saved value is =====
const EVERY = [3, 5, 10, 15, 20, 30, 60];
check('next preset up', stepTo(EVERY, 10, 1), 15);
check('next preset down', stepTo(EVERY, 10, -1), 5);
check('at the bottom', stepTo(EVERY, 3, -1), null);
check('at the top', stepTo(EVERY, 60, 1), null);
check('a hand-edited 7 goes up to 10', stepTo(EVERY, 7, 1), 10);
check('a hand-edited 7 goes down to 5', stepTo(EVERY, 7, -1), 5);
check('above every preset only goes down', [stepTo(EVERY, 120, 1), stepTo(EVERY, 120, -1)], [null, 60]);

// ===== nsLine and nsCell: what POST /api/domains/check found =====
const at = new Date(2026, 10, 4, 12, 4);
const here = { domain: 't.example.com', serves: 'feed', main: true, ns: ['ns.t.example.com'], pointsHere: true };
const elsewhere = { domain: 'c.example.com', serves: 'chat', ns: ['ns1.dns-host.net', 'ns2.dns-host.net'], pointsHere: false };
const unknown = { domain: 'm.example.com', serves: 'mtproto', pointsHere: false, error: 'lookup m.example.com: no such host' };
check('all point here', nsLine([here, here, here, here, here], at, at), 'Checked at 12:04: all 5 point to this server.');
check('the one points here', nsLine([here], at, at), 'Checked at 12:04: it points to this server.');
check('some do', nsLine([here, elsewhere, unknown], at, at), 'Checked at 12:04: 1 of 3 point to this server.');
check('cell: here', nsCell(here), ['Points here', 'sw-ok']);
check('cell: elsewhere names the servers', nsCell(elsewhere), ['Points to ns1.dns-host.net, ns2.dns-host.net', 'sw-down']);
check('cell: lookup failed', nsCell(unknown), ['No NS record found', 'sw-down']);

console.log(`${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
