// Pure helpers for the panel UI: no DOM, so node tests load this file as is
// (jstest/util.test.mjs, run by go test).
'use strict';

// plural(2, 'try', 'tries') is "2 tries".
function plural(n, one, many) { return `${n} ${n === 1 ? one : many}`; }

// when says a time the way the panel does, in local time: "09:12" today,
// "22 Oct, 07:41" earlier this year, "31 Dec 2025, 18:05" before that.
function when(t, now = new Date()) {
  const d = new Date(t), two = n => String(n).padStart(2, '0');
  const hm = `${two(d.getHours())}:${two(d.getMinutes())}`;
  if (d.toDateString() === now.toDateString()) return hm;
  const day = `${d.getDate()} ${'Jan Feb Mar Apr May Jun Jul Aug Sep Oct Nov Dec'.split(' ')[d.getMonth()]}`;
  return d.getFullYear() === now.getFullYear() ? `${day}, ${hm}` : `${day} ${d.getFullYear()}, ${hm}`;
}

// upFor says how long it's been since t, the way uptime reads: "under a
// minute", "5 minutes", "3 hours", "13 days", rounded down.
function upFor(t, now = new Date()) {
  const m = Math.floor((now - new Date(t)) / 60000);
  if (m < 1) return 'under a minute';
  if (m < 60) return plural(m, 'minute', 'minutes');
  if (m < 1440) return plural(Math.floor(m / 60), 'hour', 'hours');
  return plural(Math.floor(m / 1440), 'day', 'days');
}

// fieldErrors puts the API's {errors:[{field,message}]} onto a form's fields:
// {field: message}, with "" for the whole form, including fields it lacks.
function fieldErrors(data, fields) {
  const out = {};
  for (const e of data?.errors || []) {
    const f = fields.includes(e.field) ? e.field : '';
    out[f] ??= e.message;
  }
  return Object.keys(out).length ? out : { '': "The panel couldn't do that. Try again." };
}

// keepValue says whether a typed value survives the re-render after an error.
// Text always does; a password only when nothing says it, its repeat, or the
// whole form was wrong.
function keepValue(name, secret, errs) {
  if (!secret) return true;
  return !errs[''] && !errs[name] && !(name === 'repeat' && errs.password);
}

// clientLabel turns a session's User-Agent into "Safari on iPhone". Anything
// that isn't a known browser is shown as sent, so an odd client stands out.
// Order matters: Edge says Chrome, Chrome says Safari, iPhones say Mac OS X.
function clientLabel(ua) {
  const find = list => list.find(([re]) => re.test(ua))?.[1];
  const b = find([[/Edg\//, 'Edge'], [/Firefox\/|FxiOS/, 'Firefox'], [/Chrome\/|CriOS/, 'Chrome'], [/Safari\//, 'Safari']]);
  if (!b) return ua || 'Unknown browser';
  const os = find([[/iPhone/, 'iPhone'], [/iPad/, 'iPad'], [/Android/, 'Android'], [/Windows/, 'Windows'], [/Mac OS X/, 'macOS'], [/Linux/, 'Linux']]);
  return os ? `${b} on ${os}` : b;
}

// groupSections lists Control's sections as [group, sections] pairs: groups
// in the given order (unknown ones last), sections in the order they registered.
function groupSections(sections, order) {
  const rank = g => (order.indexOf(g) + 1 || order.length + 1);
  const groups = [...new Set(sections.map(s => s.group))].sort((a, b) => rank(a) - rank(b));
  return groups.map(g => [g, sections.filter(s => s.group === g)]);
}

// svcWords is how Service and power words what it manages, from GET
// /api/service: thefeed-server as a systemd unit, or as a Docker container.
function svcWords(svc) {
  const docker = svc.server === 'docker';
  return {
    title: `${docker ? 'Container' : 'Service'} ${esc(svc.name)}`,
    boot: docker ? 'Start with Docker' : 'Start when the machine boots',
    // restart policy unless-stopped: a container stopped by hand stays stopped
    bootOn: docker ? 'Docker starts it after a reboot, unless you stopped it.' : 'systemd starts it after a reboot.',
    shadow: docker ? 'Settings in docker-compose.yml' : 'Settings passed as flags',
    shadowWhy: docker
      ? "thefeed-server's container gets these from docker-compose.yml, and they beat thefeed.env, so changes made here to them don't apply. Remove them from docker-compose.yml, then run <code>docker compose up -d</code>."
      : "thefeed-server's systemd unit passes these, and a flag beats thefeed.env, so changes made here to them don't apply.",
  };
}

// esc makes any value safe to put in HTML text or a quoted attribute.
function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
