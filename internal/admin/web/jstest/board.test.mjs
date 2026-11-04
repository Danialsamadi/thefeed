// Unit tests for the Board, feature and Server pages' pure helpers
// (static/js/board-util.js). Zero dependencies; run with
// node internal/admin/web/jstest/board.test.mjs, or by go test
// ./internal/admin/web (TestJSUtil). Dates are built in local time, so the
// tests pass in any time zone.
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import vm from 'node:vm';

const js = join(dirname(fileURLToPath(import.meta.url)), '..', 'static', 'js');
for (const f of ['util.js', 'board-util.js']) vm.runInThisContext(readFileSync(join(js, f), 'utf8'), { filename: f });

let failed = 0, passed = 0;
function check(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { passed++; return; }
  failed++;
  console.error(`FAIL ${name}\n  got:  ${g}\n  want: ${w}`);
}

// ===== upBars: the API's day states as the bars' classes (green up, amber slow, red down, grey off) =====
check('each state', upBars(['up', 'slow', 'down', 'off']), ['', 'w', 'x', 'n']);
check('unknown reads as off', upBars(['up', 'later']), ['', 'n']);

// ===== upPct: the uptime percentage as the row and the X facts write it =====
check('all up', upPct(100), '100%');
check('one decimal', [upPct(61.7), upPct(99)], ['61.7%', '99.0%']);
check('every day off: no percentage', upPct(null), '—');

// ===== upLegend: the 60 days' counts written out under the bars =====
const days = (n, s) => Array(n).fill(s);
check('the mockup\'s X', upLegend({ days: [...days(30, 'up'), 'slow', 'slow', 'up', 'up', 'slow', ...days(2, 'up'), ...days(23, 'down')], up: 34, slow: 3, down: 23, off: 0 }),
  '34 days up, 3 slow, 23 down');
check('off only before it was turned on', upLegend({ days: [...days(30, 'off'), ...days(29, 'up'), 'slow'], up: 29, slow: 1, down: 0, off: 30 }),
  '29 days up, 1 slow, 30 before it was turned on');
check('off in between is just off', upLegend({ days: ['off', 'up', 'off', 'up'], up: 2, slow: 0, down: 0, off: 2 }), '2 days up, 2 off');
check('one day', upLegend({ days: ['up'], up: 1, slow: 0, down: 0, off: 0 }), '1 day up');
check('the first count says days', upLegend({ days: ['down', 'slow'], up: 0, slow: 1, down: 1, off: 0 }), '1 day slow, 1 down');
check('every day off', upLegend({ days: days(60, 'off'), up: 0, slow: 0, down: 0, off: 60 }), '60 days off');

// ===== axis labels: under the 60-day bars, and under the Server charts (first point, middle point, now) =====
const now = new Date(2026, 10, 3, 12, 4, 31);
check('60 days to today', uptimeAxis(now), ['5 Sep', '4 Oct', 'today']);
check('across a new year', uptimeAxis(new Date(2026, 0, 10)), ['12 Nov', '11 Dec', 'today']);
check('1 h, a minute a point', chartAxis(new Date(2026, 10, 3, 11, 5), 60, now), ['11:05', '11:35', 'now']);
check('15 min', chartAxis(new Date(2026, 10, 3, 11, 49, 45).toISOString(), 15, now), ['11:49', '11:57', 'now']);
check('24 h starts yesterday', chartAxis(new Date(2026, 10, 2, 12, 24), 1440, now), ['yesterday 12:24', '00:24', 'now']);
check('7 days shows dates', chartAxis(new Date(2026, 9, 27, 12, 48), 10080, now), ['27 Oct', '31 Oct', 'now']);
check('what a bar stands for, by range', [perStep(15), perStep(60), perStep(1440), perStep(10080)],
  ['per 15 seconds', 'per minute', 'per 24 minutes', 'per 2.8 hours']);

// ===== barPct: a chart bar's height, as a share of the chart's top =====
check('half the scale', barPct(3, 6), 50);
check('over the top stays in the chart', barPct(9, 6), 100);
check('CPU is out of 100, to one decimal', barPct(23.456, 100), 23.5);
check('no sample: no bar', barPct(null, 6), null);
check('never below the axis', barPct(-1, 100), 0);

// ===== inUnit: bytes as the Server page and the pinned line write them =====
const MiB = 2 ** 20, GiB = 2 ** 30;
check('RAM in whole MB', [inUnit(412 * MiB, 'MB'), inUnit(286.4 * MiB, 'MB')], ['412', '286']);
check('thousands get a comma', inUnit(GiB, 'MB'), '1,024');
check('disk in GB to one decimal', [inUnit(7.6 * GiB, 'GB'), inUnit(12.44 * GiB, 'GB')], ['7.6', '12.4']);
check('a whole GB has no decimal', inUnit(20 * GiB, 'GB'), '20');

// ===== lasted: an incident's "Lasted" cell =====
const at = (d, h, m, s = 0) => new Date(2026, 10, d, h, m, s);
check('minutes', lasted(at(3, 12, 4), at(3, 12, 10)), '6 min');
check('hours and minutes', lasted(at(3, 10, 0), at(3, 12, 5)), '2 h 5 min');
check('whole hours, past a day', lasted(at(2, 10, 0), at(3, 12, 0)), '26 h');
check('days from two on', lasted(at(1, 12, 0), at(3, 12, 0)), '2 days');
check('ongoing counts to now', lasted(new Date(2026, 9, 12, 9, 40), null, now), '22 days, ongoing');
check('short', lasted(at(3, 12, 4), at(3, 12, 4, 30)), 'under a minute');

// ===== log days: the API groups lines by local day ("2026-11-03"); the page heads each group =====
check('today', dayLabel('2026-11-03', now), 'Today');
check('earlier this year', dayLabel('2026-10-12', now), '12 Oct');
check('last year says so', dayLabel('2025-12-31', now), '31 Dec 2025');
check('a day is local, not UTC midnight', dayLabel('2026-11-01', new Date(2026, 10, 1, 0, 30)), 'Today');
check('a line\'s time', hms(new Date(2026, 10, 3, 9, 4, 2).toISOString()), '09:04:02');

// ===== the Nitter editor: lines typed, stored the way the server stores them (serverconfig.ParseInstance) =====
check('a bare host gets https', instanceName(' nitter.net '), 'https://nitter.net');
check('scheme kept, case and a trailing slash dropped', instanceName('HTTP://Nitter.Example.com/'), 'http://nitter.example.com');

// ===== nextInstanceOp: the one list change that brings the saved instances closer to the typed ones =====
const [a, b, c] = ['https://a.example', 'https://b.example', 'https://c.example'];
check('adds first, so one is always left', nextInstanceOp([a, b], [b, c]), { op: 'add', value: c });
check('adds in the typed order', nextInstanceOp([a], [b, c]), { op: 'add', value: b });
check('then removes', nextInstanceOp([a, b, c], [b, c]), { op: 'remove', index: 0 });
check('then moves into place', nextInstanceOp([b, c, a], [a, b, c]), { op: 'move', index: 2, to: 0 });
check('done', nextInstanceOp([a, b], [a, b]), null);

if (failed) { console.error(`${failed} failed, ${passed} passed`); process.exit(1); }
console.log(`${passed} passed`);
