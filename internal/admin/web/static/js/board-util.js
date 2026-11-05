// The Board, feature and Server pages' pure helpers: no DOM, so node tests
// load this file as is (jstest/board.test.mjs, run by go test). Uses plural
// and when from util.js.
'use strict';

// upBars turns the API's 60 day states into the bars' classes: '' up,
// w slow, x down, n off (and anything else, so a gap never reads as up).
function upBars(days) {
  return days.map(d => ({ up: '', slow: 'w', down: 'x' }[d] ?? 'n'));
}

// upPct writes an uptime percentage the way the server does in X's facts:
// 100% or 61.7%, and a dash when every day was off.
function upPct(p) {
  if (p == null) return '—';
  return p === 100 ? '100%' : `${p.toFixed(1)}%`;
}

// upLegend writes out uptime u's counts: "34 days up, 3 slow, 23 down". Off
// days that all come first were before it was turned on.
function upLegend(u) {
  const first = u.days.findIndex(d => d !== 'off'), early = first > 0 && !u.days.slice(first).includes('off');
  const parts = [[u.up, 'up'], [u.slow, 'slow'], [u.down, 'down'], [u.off, early ? 'before it was turned on' : 'off']].filter(([n]) => n);
  return parts.map(([n, what], i) => (i ? `${n} ${what}` : `${plural(n, 'day', 'days')} ${what}`)).join(', ');
}

const two = n => String(n).padStart(2, '0');
const hm = d => `${two(d.getHours())}:${two(d.getMinutes())}`;
const dayMonth = d => `${d.getDate()} ${'Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec'.split(' ')[d.getMonth()]}`;

// uptimeAxis labels the 60 daily bars: the first day, the middle one, today.
function uptimeAxis(now = new Date()) {
  const ago = n => { const d = new Date(now); d.setDate(d.getDate() - n); return dayMonth(d); };
  return [ago(59), ago(30), 'today'];
}

// chartAxis labels a Server chart of 60 points of step seconds from from:
// its first point, its middle one and now. Times for a day or less, with
// "yesterday" when it starts before today; dates for longer.
function chartAxis(from, step, now = new Date()) {
  const a = new Date(from), mid = new Date(a.getTime() + 30 * step * 1000);
  if (60 * step > 86400) return [dayMonth(a), dayMonth(mid), 'now'];
  const label = d => (d.toDateString() === now.toDateString() ? hm(d) : `yesterday ${hm(d)}`);
  return [label(a), label(mid), 'now'];
}

// perStep says what one chart bar of step seconds stands for: "per minute".
function perStep(step) {
  if (step < 60) return `per ${step} seconds`;
  if (step === 60) return 'per minute';
  return step < 3600 ? `per ${step / 60} minutes` : `per ${+(step / 3600).toFixed(1)} hours`;
}

// barPct is a chart bar's height in percent of the chart's top, or null
// for a point without a sample.
function barPct(v, top) {
  if (v == null) return null;
  return Math.round(Math.min(100, Math.max(0, v / top * 100)) * 10) / 10;
}

// inUnit writes bytes b in MB (whole) or GB (one decimal), as the number
// alone: the page adds the unit where it goes.
function inUnit(b, unit) {
  return unit === 'MB' ? Math.round(b / 2 ** 20).toLocaleString('en') : (b / 2 ** 30).toLocaleString('en', { maximumFractionDigits: 1 });
}

// rate writes a network rate given in Mb/s as [number, unit]: kb/s under
// 1 Mb/s, where a feed server mostly runs and one decimal of Mb/s reads 0.0.
function rate(mbps) {
  const kb = Math.round(mbps * 1000);
  return kb < 1000 ? [String(kb), 'kb/s'] : [mbps.toFixed(1), 'Mb/s'];
}

// span writes a length of time ms the way incidents and states do: "6 min",
// "2 h 5 min", "26 h", "22 days", rounded down.
function span(ms) {
  const m = Math.floor(ms / 60000), h = Math.floor(m / 60);
  if (m < 1) return 'under a minute';
  if (m < 60) return `${m} min`;
  if (h < 48) return m % 60 ? `${h} h ${m % 60} min` : `${h} h`;
  return plural(Math.floor(h / 24), 'day', 'days');
}

// lasted is how long an incident from start to end lasted, or has lasted
// so far when it hasn't ended.
function lasted(start, end, now = new Date()) {
  return end ? span(new Date(end) - new Date(start)) : `${span(now - new Date(start))}, ongoing`;
}

// dayLabel heads a log day, "2026-11-03" in the panel's time zone: Today,
// "12 Oct", or "31 Dec 2025" before this year.
function dayLabel(day, now = new Date()) {
  const [y, m, d] = day.split('-').map(Number), t = new Date(y, m - 1, d);
  if (t.toDateString() === now.toDateString()) return 'Today';
  return y === now.getFullYear() ? dayMonth(t) : `${dayMonth(t)} ${y}`;
}

// instanceName is a typed Nitter instance as the server stores it
// (serverconfig.ParseInstance): scheme://host, https when bare, lower case.
// The server still validates it when it's added.
function instanceName(raw) {
  const s = raw.trim();
  return (s.includes('://') ? s : 'https://' + s).toLowerCase().replace(/\/$/, '');
}

// nextInstanceOp is the one list change, as POST /api/lists/instances takes
// it, that brings the saved instances items closer to want, or null when
// they match. Adds come first, so removing never leaves the list empty.
function nextInstanceOp(items, want) {
  const add = want.find(w => !items.includes(w));
  if (add) return { op: 'add', value: add };
  const rm = items.findIndex(it => !want.includes(it));
  if (rm >= 0) return { op: 'remove', index: rm };
  const i = items.findIndex((it, i) => it !== want[i]);
  return i < 0 ? null : { op: 'move', index: items.indexOf(want[i]), to: i };
}

// hms is a log line's time of day.
const hms = at => { const d = new Date(at); return `${hm(d)}:${two(d.getSeconds())}`; };
