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

// linePaths is a Server chart's points as SVG path data on a viewBox
// (points − 1) wide and 100 tall, 0 at the top: line runs through the
// points as a smooth curve, broken where one is null, and area closes each
// run down to the baseline along the same curve. A lone point between gaps
// becomes a short flat stroke, so it still shows.
function linePaths(points, top) {
  const y = v => 100 - Math.min(100, Math.max(0, v / top * 100)), last = points.length - 1, f = n => +n.toFixed(2);
  let line = '', area = '', run = [];
  const flush = () => {
    if (run.length === 1) run = [[Math.max(0, run[0][0] - 0.4), run[0][1]], [Math.min(last, run[0][0] + 0.4), run[0][1]]];
    if (run.length) {
      const [x0, y0] = run[0], d = curve(run), xn = run.at(-1)[0];
      line += `M${f(x0)} ${f(y0)}${d}`;
      area += `M${f(x0)} 100L${f(x0)} ${f(y0)}${d}L${f(xn)} 100Z`;
    }
    run = [];
  };
  points.forEach((v, i) => (v == null ? flush() : run.push([i, y(v)])));
  flush();
  return { line, area };
}

// curve is the path on from the first of pts ([x, y], two or more) through
// the rest: a straight line for two, else a monotone cubic (Fritsch–Carlson,
// as d3's curveMonotoneX). Each piece stays between its two points, so the
// line never overshoots the chart or shows a peak the samples don't have.
function curve(pts) {
  const n = pts.length, f = v => +v.toFixed(2);
  if (n === 2) return `L${f(pts[1][0])} ${f(pts[1][1])}`;
  const s = i => (pts[i + 1][1] - pts[i][1]) / (pts[i + 1][0] - pts[i][0]); // the slope from point i to i + 1
  const m = pts.map((_, i) => (i === 0 || i === n - 1 ? 0
    : (Math.sign(s(i - 1)) + Math.sign(s(i))) * Math.min(Math.abs(s(i - 1)), Math.abs(s(i)), Math.abs(s(i - 1) + s(i)) / 4) || 0));
  m[0] = (3 * s(0) - m[1]) / 2;
  m[n - 1] = (3 * s(n - 2) - m[n - 2]) / 2;
  let d = '';
  for (let i = 0; i < n - 1; i++) {
    const [x0, y0] = pts[i], [x1, y1] = pts[i + 1], dx = (x1 - x0) / 3;
    d += `C${f(x0 + dx)} ${f(y0 + dx * m[i])} ${f(x1 - dx)} ${f(y1 - dx * m[i + 1])} ${f(x1)} ${f(y1)}`;
  }
  return d;
}

// niceMax is the CPU chart's top for a peak of peak percent: the smallest
// of 5, 10, 25, 50 and 100 that leaves a tenth of room above the peak.
function niceMax(peak) {
  return [5, 10, 25, 50, 100].find(t => t >= (peak ?? 0) * 1.1) ?? 100;
}

// pointLabel is when point i of a chart of step-second points from from
// was: to the second for 15-second points, else as when() writes it.
function pointLabel(from, step, i, now = new Date()) {
  const t = new Date(new Date(from).getTime() + i * step * 1000);
  return step < 60 ? `${hm(t)}:${two(t.getSeconds())}` : when(t, now);
}

// barPct is a bar's length in percent of its whole, or null for a point
// without a sample.
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

// logExport is the links that save a log as a .log file: the last hour,
// day or week of feature id's lines ("server" for every line), filtered as
// the page shows them (GET /api/logs/{id}/export).
function logExport(id, filter = 'all', q = '') {
  const query = `filter=${filter}${q ? `&q=${encodeURIComponent(q)}` : ''}`;
  return `<div class="lgexp"><span>Export the last</span>${[[1, '1 hour'], [24, '24 hours'], [168, '7 days']].map(([h, label]) =>
    `<a class="btn quiet" href="/api/logs/${id}/export?${query}&hours=${h}" download data-k="exp-${id}-${h}" aria-label="Export the last ${label} of this log as a .log file">${label}</a>`).join('')}</div>`;
}

// hms is a log line's time of day.
const hms = at => { const d = new Date(at); return `${hm(d)}:${two(d.getSeconds())}`; };
