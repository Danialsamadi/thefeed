// Unit tests for the frontend link-handling helpers (parseTgLink,
// feedLinkTarget, findChannelByUsername). Zero dependencies — run with:
//   node internal/web/jstest/links.test.mjs
// Also wired into `go test ./internal/web` (TestJSLinkUnit).
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import vm from 'node:vm';

const jsDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'static', 'js');

// Minimal DOM stub — enough for the scripts' top-level statements
// (DOMContentLoaded registration); the functions under test are pure.
globalThis.document = {
  addEventListener() { },
  getElementById() { return null; },
};
globalThis.window = globalThis;

for (const f of ['ui.js', 'messages.js']) {
  vm.runInThisContext(readFileSync(join(jsDir, f), 'utf8'), { filename: f });
}

let failed = 0, passed = 0;
function check(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { passed++; return; }
  failed++;
  console.error(`FAIL ${name}\n  got:  ${g}\n  want: ${w}`);
}

// ===== parseTgLink =====
const P = globalThis.parseTgLink;
check('channel link', P('https://t.me/somechannel'), { user: 'somechannel', postId: '' });
check('post link', P('https://t.me/somechannel/76737'), { user: 'somechannel', postId: '76737' });
check('telegram.me host', P('http://telegram.me/somechannel/5'), { user: 'somechannel', postId: '5' });
check('trailing slash', P('https://t.me/somechannel/'), { user: 'somechannel', postId: '' });
check('post + trailing slash', P('https://t.me/somechannel/76737/'), { user: 'somechannel', postId: '76737' });
check('query string', P('https://t.me/somechannel/76737?single'), { user: 'somechannel', postId: '76737' });
check('fragment', P('https://t.me/somechannel/76737#comment-1'), { user: 'somechannel', postId: '76737' });
check('query + fragment', P('https://t.me/somechannel?before=9#x'), { user: 'somechannel', postId: '' });
check('/s/ preview link', P('https://t.me/s/somechannel/76737'), { user: 'somechannel', postId: '76737' });
check('/s/ channel only', P('https://t.me/s/somechannel'), { user: 'somechannel', postId: '' });
check('underscore user', P('https://t.me/some_channel_1/2'), { user: 'some_channel_1', postId: '2' });

check('special: joinchat', P('https://t.me/joinchat'), null);
check('special: joinchat invite', P('https://t.me/joinchat/AbCdEf123'), null);
check('special: proxy', P('https://t.me/proxy?server=1.2.3.4&port=443'), null);
check('special: share', P('https://t.me/share?url=x'), null);
check('special: private /c/ link', P('https://t.me/c/1234567'), null);
check('special: boost', P('https://t.me/boost/somechannel'), null);
check('too-short user', P('https://t.me/abc'), null);
check('digit-first user', P('https://t.me/1abc'), null);
check('other domain', P('https://example.com/somechannel/1'), null);
check('t.me lookalike', P('https://not-t.me/somechannel'), null);
check('no scheme', P('t.me/somechannel'), null);
check('extra path segment', P('https://t.me/somechannel/76737/extra'), null);
check('non-numeric post', P('https://t.me/somechannel/abc'), null);
check('empty input', P(''), null);
check('null input', P(null), null);

// ===== findChannelByUsername (case/@ handling) =====
globalThis.channels = [
  { Name: 'somechannel' },
  { name: '@Other_News' },
];
const F = globalThis.findChannelByUsername;
check('channel lookup exact', F('somechannel'), 1);
check('channel lookup case', F('SomeChannel'), 1);
check('channel lookup @ prefix', F('@somechannel'), 1);
check('channel lookup stored-@', F('other_news'), 2);
check('channel lookup miss', F('nosuch'), 0);

// ===== feedLinkTarget =====
const T = globalThis.feedLinkTarget;
check('mention in list', T('SomeChannel', 'https://t.me/SomeChannel'),
  { user: 'SomeChannel', postId: '', chNum: 1 });
check('mention not in list', T('stranger1', 'https://t.me/stranger1'),
  { user: 'stranger1', postId: '', chNum: 0 });
check('channel link in list', T(null, 'https://t.me/somechannel'),
  { user: 'somechannel', postId: '', chNum: 1 });
check('post link in list', T(null, 'https://t.me/somechannel/76737'),
  { user: 'somechannel', postId: '76737', chNum: 1 });
check('post link not in list', T(null, 'https://t.me/stranger1/9'),
  { user: 'stranger1', postId: '9', chNum: 0 });
check('non-telegram link', T(null, 'https://example.com/a'), null);
check('special path link', T(null, 'https://t.me/joinchat/XyZ'), null);

if (failed) {
  console.error(`\n${failed} failed, ${passed} passed`);
  process.exit(1);
}
console.log(`ok — ${passed} checks passed`);
