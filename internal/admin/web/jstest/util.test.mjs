// Unit tests for the panel UI's pure helpers (static/js/util.js). Zero
// dependencies; run with node internal/admin/web/jstest/util.test.mjs, or by
// go test ./internal/admin/web (TestJSUtil).
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import vm from 'node:vm';

const file = join(dirname(fileURLToPath(import.meta.url)), '..', 'static', 'js', 'util.js');
vm.runInThisContext(readFileSync(file, 'utf8'), { filename: 'util.js' });

let failed = 0, passed = 0;
function check(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { passed++; return; }
  failed++;
  console.error(`FAIL ${name}\n  got:  ${g}\n  want: ${w}`);
}

// ===== plural =====
check('plural one', plural(1, 'try', 'tries'), '1 try');
check('plural many', plural(2, 'try', 'tries'), '2 tries');
check('plural zero', plural(0, 'session', 'sessions'), '0 sessions');

// ===== esc: everything from the API goes through it into innerHTML =====
check('esc markup', esc('<img src=x onerror="a()">'), '&lt;img src=x onerror=&quot;a()&quot;&gt;');
check('esc amp and quote', esc(`Tom & Jerry's`), 'Tom &amp; Jerry&#39;s');
check('esc number', esc(42), '42');
check('esc null', esc(null), '');

// ===== when: local time, the date only when it isn't today =====
const now = new Date(2026, 10, 4, 12, 4);
check('when today', when(new Date(2026, 10, 4, 9, 2), now), '09:02');
check('when earlier this year', when(new Date(2026, 9, 22, 7, 41), now), '22 Oct, 07:41');
check('when yesterday', when(new Date(2026, 10, 3, 23, 59), now), '3 Nov, 23:59');
check('when last year', when(new Date(2025, 11, 31, 18, 5), now), '31 Dec 2025, 18:05');
check('when from an API string', when(new Date(2026, 10, 4, 0, 0).toISOString(), now), '00:00');

// ===== fieldErrors: the API's {errors:[{field,message}]} onto a form =====
const setupFields = ['code', 'username', 'password', 'repeat'];
check('field errors by field', fieldErrors({ errors: [
  { field: 'code', message: "That code doesn't match. Use the one the installer printed." },
  { field: 'password', message: 'Use at least 12 characters.' },
] }, setupFields), { code: "That code doesn't match. Use the one the installer printed.", password: 'Use at least 12 characters.' });
check('form-level error', fieldErrors({ errors: [{ field: '', message: 'Wrong username or password.' }], triesLeft: 4 }, ['username', 'password']),
  { '': 'Wrong username or password.' });
check('a field the form lacks lands on the form', fieldErrors({ errors: [{ field: 'current', message: 'x' }] }, setupFields), { '': 'x' });
check('first message per field wins', fieldErrors({ errors: [{ field: 'code', message: 'a' }, { field: 'code', message: 'b' }] }, setupFields), { code: 'a' });
check('no error body', fieldErrors({}, setupFields), { '': "The panel couldn't do that. Try again." });

// ===== keepValue: typed values survive an error, except passwords that were wrong =====
const wrongCode = { code: "That code doesn't match." };
check('wrong code: keep the code to fix it', keepValue('code', false, wrongCode), true);
check('wrong code: keep the good password', keepValue('password', true, wrongCode), true);
check('wrong code: keep the good repeat', keepValue('repeat', true, wrongCode), true);
const shortPass = { password: 'Use at least 12 characters.' };
check('short password: clear it', keepValue('password', true, shortPass), false);
check('short password: clear its repeat too', keepValue('repeat', true, shortPass), false);
check('short password: keep the username', keepValue('username', false, shortPass), true);
const mismatch = { repeat: "The passwords don't match." };
check('mismatch: keep the password', keepValue('password', true, mismatch), true);
check('mismatch: clear the repeat', keepValue('repeat', true, mismatch), false);
const wrongLogin = { '': 'Wrong username or password.' };
check('wrong login: keep the username', keepValue('username', false, wrongLogin), true);
check('wrong login: clear the password', keepValue('password', true, wrongLogin), false);
check('wrong current password: clear it', keepValue('current', true, { current: "That isn't the current password." }), false);
check('wrong current password: keep the new one', keepValue('password', true, { current: "That isn't the current password." }), true);

// ===== clientLabel: the session list names the browser, not its User-Agent =====
check('chrome on mac', clientLabel('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36'), 'Chrome on macOS');
check('safari on iphone', clientLabel('Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Mobile/15E148 Safari/604.1'), 'Safari on iPhone');
check('safari on ipad', clientLabel('Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1'), 'Safari on iPad');
check('chrome on iphone', clientLabel('Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0.6723.90 Mobile/15E148 Safari/604.1'), 'Chrome on iPhone');
check('firefox on windows', clientLabel('Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0'), 'Firefox on Windows');
check('edge on windows', clientLabel('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0'), 'Edge on Windows');
check('chrome on android', clientLabel('Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36'), 'Chrome on Android');
check('firefox on linux', clientLabel('Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0'), 'Firefox on Linux');
check('not a browser: as sent', clientLabel('curl/8.4.0'), 'curl/8.4.0');
check('nothing sent', clientLabel(''), 'Unknown browser');

// ===== groupSections: Control's groups in a fixed order, whatever order pages load in =====
const ORDER = ['Server', 'Sources', 'Features', 'Server settings', 'This panel'];
const names = gs => gs.map(([g, ss]) => [g, ss.map(s => s.id)]);
check('only this panel', names(groupSections([{ group: 'This panel', id: 'acct' }, { group: 'This panel', id: 'look' }], ORDER)),
  [['This panel', ['acct', 'look']]]);
check('later pages slot into their group', names(groupSections([
  { group: 'This panel', id: 'acct' }, { group: 'This panel', id: 'look' },
  { group: 'Sources', id: 'chan' }, { group: 'Features', id: 'chat' }, { group: 'Sources', id: 'xacc' },
  { group: 'Server', id: 'svc' },
], ORDER)), [['Server', ['svc']], ['Sources', ['chan', 'xacc']], ['Features', ['chat']], ['This panel', ['acct', 'look']]]);
check('an unknown group goes last', names(groupSections([{ group: 'Extras', id: 'x' }, { group: 'This panel', id: 'acct' }], ORDER)),
  [['This panel', ['acct']], ['Extras', ['x']]]);

// ===== upFor: "Up 3 days" on Service and power =====
const up = new Date(2026, 9, 22, 7, 58);
check('just started', upFor(up, new Date(2026, 9, 22, 7, 58, 40)), 'under a minute');
check('one minute', upFor(up, new Date(2026, 9, 22, 7, 59, 30)), '1 minute');
check('minutes', upFor(up, new Date(2026, 9, 22, 8, 57)), '59 minutes');
check('an hour', upFor(up, new Date(2026, 9, 22, 8, 58)), '1 hour');
check('hours, rounded down', upFor(up, new Date(2026, 9, 23, 7, 57)), '23 hours');
check('a day', upFor(up, new Date(2026, 9, 23, 7, 58)), '1 day');
check('days', upFor('2026-10-22T07:58:00Z', new Date('2026-11-04T12:04:00Z')), '13 days');

console.log(`${passed} passed, ${failed} failed`);
if (failed) process.exit(1);
