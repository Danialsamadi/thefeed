// ===== SEND =====
async function sendMessage() {
  var input = document.getElementById('sendInput'); var text = input.value.trim();
  if (!text || !selectedChannel) return;
  try {
    var r = await fetch('/api/send', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ channel: selectedChannel, text: text }) });
    if (r.ok) { input.value = ''; addLogLine('Message sent') }
    else addLogLine('Error: ' + (await r.text()));
  } catch (e) { addLogLine('Error: ' + e.message) }
}

// ===== COPY MSG =====
// ===== DONATE MODAL =====
function openDonate() { document.getElementById('donateModal').classList.add('active'); }
function closeDonate() { document.getElementById('donateModal').classList.remove('active'); }
function openSource() { document.getElementById('sourceModal').classList.add('active'); }
function closeSource() { document.getElementById('sourceModal').classList.remove('active'); }
function openPrivacy() { document.getElementById('privacyModal').classList.add('active'); }
function closePrivacy() { document.getElementById('privacyModal').classList.remove('active'); }
function copyDonateAddr() {
  var el = document.getElementById('donateAddr');
  var v = (el.dataset.full || el.textContent).trim();
  navigator.clipboard.writeText(v).then(function () { showToast(t('copied')) }).catch(function () { });
}

function copyMsg(idx) {
  var text = currentMsgTexts[idx]; if (text === undefined) return;
  navigator.clipboard.writeText(text).then(function () { showToast(t('msg_copied')) }).catch(function () { });
}

// ===== SCROLL TO BOTTOM + FLOATING DATE =====
(function () {
  var messagesEl = null, dateEl = null, hideTimer = null;
  function topMostDate() {
    if (!messagesEl) return '';
    var seps = messagesEl.querySelectorAll('.msg-date-sep');
    if (!seps.length) return '';
    var top = messagesEl.getBoundingClientRect().top;
    var current = '';
    for (var i = 0; i < seps.length; i++) {
      var r = seps[i].getBoundingClientRect();
      // The most recent separator at or above the viewport's top
      // edge is the date the user is currently reading under.
      if (r.top - top <= 8) current = seps[i].textContent;
      else break;
    }
    return current.trim();
  }
  function showFloatingDate() {
    if (!dateEl) return;
    var s = topMostDate();
    if (!s) { dateEl.classList.remove('visible'); dateEl.hidden = true; return; }
    dateEl.textContent = s;
    // Force RTL for Persian/Arabic content so the day-month-year
    // sequence reads right-to-left as expected (16 اردیبهشت 1405,
    // not 1405 اردیبهشت 16).
    dateEl.dir = /[؀-ۿ]/.test(s) ? 'rtl' : 'ltr';
    dateEl.hidden = false;
    dateEl.classList.add('visible');
    if (hideTimer) clearTimeout(hideTimer);
    hideTimer = setTimeout(function () { dateEl.classList.remove('visible'); }, 1200);
  }
  function initScrollBtn() {
    messagesEl = document.getElementById('messages');
    dateEl = document.getElementById('floatingDate');
    if (!messagesEl) return;
    messagesEl.addEventListener('scroll', function () {
      var atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 150;
      document.getElementById('scrollDownBtn').classList.toggle('visible', !atBottom);
      showFloatingDate();
    });
  }
  // Init after DOM is ready
  document.addEventListener('DOMContentLoaded', initScrollBtn, { once: true });
})();
function scrollToBottom() {
  var el = document.getElementById('messages');
  if (el) el.scrollTop = el.scrollHeight;
}

// ===== TOAST =====
var _toastTimer = null;
function showToast(msg, ms) {
  var el = document.getElementById('toast');
  el.textContent = msg;
  el.classList.add('show');
  if (_toastTimer) clearTimeout(_toastTimer);
  _toastTimer = setTimeout(function () { el.classList.remove('show'); _toastTimer = null; }, ms || 2200);
}
// hideToast dismisses the current toast early — used when a long-lived
// progress toast ("finding the post…") is resolved by success.
function hideToast() {
  var el = document.getElementById('toast');
  if (el) el.classList.remove('show');
  if (_toastTimer) { clearTimeout(_toastTimer); _toastTimer = null; }
}

// ===== UTILITIES =====
function formatTitleWithFlag(s) {
  return esc(s).replace(/\uD83C\uDDEE\uD83C\uDDF7/g, '<img src="/static/lion-sun.svg" alt="\u{1F981}\u2600\uFE0F" style="height:1em;vertical-align:middle">');
}
function esc(s) { var d = document.createElement('div'); d.appendChild(document.createTextNode(s)); return d.innerHTML }
function escAttr(s) { return esc(s).replace(/"/g, '&quot;').replace(/'/g, '&#39;') }
// jsStr: safe to drop into a single-quoted JS string inside a double-quoted HTML
// attribute — onclick="fn('VALUE')". esc()/escAttr() do NOT make a value
// JS-safe: escAttr turns ' into &#39;, which the HTML parser decodes back to '
// BEFORE the JS runs, re-opening the string. Backslash-escape ' (survives HTML
// decode) and neutralise " so it can't close the attribute. Use for any
// user/import-controlled value placed in an inline handler (resolver addrs,
// list/config names, etc.).
function jsStr(s) {
  return esc(String(s == null ? '' : s)).replace(/\\/g, '\\\\').replace(/'/g, "\\'").replace(/"/g, '&quot;');
}
function linkify(raw) {
  // Accepts raw (unescaped) text. Handles [label](url) markdown links,
  // plain URLs, and @username mentions.
  var result = '', last = 0, m;
  var re = /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)|(https?:\/\/[^\s<>"']+)|@([A-Za-z_][A-Za-z0-9_]{3,31})/g;
  while ((m = re.exec(raw)) !== null) {
    result += esc(raw.slice(last, m.index));
    if (m[2]) {
      result += '<a href="' + escAttr(m[2]) + '" target="_blank" rel="noopener" dir="ltr">' + esc(m[1]) + '</a>';
    } else if (m[3]) {
      var url = m[3], trail = '';
      while (url.length > 1) {
        var ch = url[url.length - 1];
        if (ch === ')' && url.split('(').length <= url.split(')').length - 1) {
          trail = ch + trail; url = url.slice(0, -1);
        } else if (/[.,;:!?>\u200C\u200F]/.test(ch)) {
          trail = ch + trail; url = url.slice(0, -1);
        } else { break; }
      }
      result += '<a href="' + escAttr(url) + '" target="_blank" rel="noopener" dir="ltr">' + esc(url) + '</a>' + esc(trail);
    } else if (m[4]) {
      result += '<a class="mention" data-mention="' + escAttr(m[4]) + '" href="https://t.me/' + escAttr(m[4]) + '">@' + esc(m[4]) + '</a>';
    }
    last = m.index + m[0].length;
  }
  result += esc(raw.slice(last));
  return result;
}
function findChannelByUsername(username) {
  var clean = username.replace(/^@/, '').toLowerCase();
  for (var i = 0; i < channels.length; i++) {
    var n = (channels[i].Name || channels[i].name || '').replace(/^@/, '').toLowerCase();
    if (n === clean) return i + 1;
  }
  return 0;
}
// parseTgLink parses a t.me / telegram.me URL → { user, postId } or null.
// Handles /s/ web-preview links, trailing slashes, query strings and
// fragments. Special Telegram paths (proxy, share, joinchat, …) are not
// channel links. Shared by the feed and telemirror link handlers.
var _tgSpecialPaths = /^(?:proxy|socks|share|addstickers|addemoji|addtheme|setlanguage|login|confirmphone|iv|joinchat|addlist|boost|contact|passport|premium|giftcode|invoice|stars|m|dl|bg|c)$/i;
function parseTgLink(url) {
  var m = String(url || '').match(/^https?:\/\/(?:t\.me|telegram\.me)\/(?:s\/)?([A-Za-z_][A-Za-z0-9_]{3,31})(?:\/(\d+))?\/?(?:\?[^#]*)?(?:#.*)?$/);
  if (!m) return null;
  if (_tgSpecialPaths.test(m[1])) return null;
  return { user: m[1], postId: m[2] || '' };
}
// gotoChannelPost opens a feed channel and, when msgId is given, scrolls to
// that post once messages render. Retries for a few seconds because the
// post may only arrive with the background refresh that follows selection.
function gotoChannelPost(chNum, msgId) {
  // Re-select even for the current channel when the pane isn't showing its
  // messages (e.g. the Saved view rendered its cards into #messages and
  // closeSavedMessages only strips the class) — selectChannel restores it.
  var mEl = document.getElementById('messages');
  var paneOk = mEl && !mEl.classList.contains('saved-mode') && mEl.querySelector('.msg');
  var reselected = !(chNum === selectedChannel && paneOk);
  var p = reselected ? selectChannel(chNum) : Promise.resolve();
  if (!msgId) return;
  var done = false;
  function finish(found) {
    if (done) return;
    done = true;
    if (found) { if (reselected) hideToast(); return; } // clear the sticky "finding…"
    if (reselected) hideToast();
    // Stay silent if the user already navigated elsewhere.
    if (selectedChannel === chNum) showToast(t('msg_not_in_cache') || 'Message no longer in cache');
  }
  // The verdict is timer-guaranteed: even if selectChannel hangs on a slow
  // refresh or a retry step dies, the user gets the error — a lone
  // "finding…" with no outcome reads as "no error shown".
  var deadline = reselected ? 8000 : 1500;
  setTimeout(function () { finish(false); }, deadline);
  // Sticky for the whole wait: the default 2.2s toast faded mid-search and
  // the silent gap until the verdict read as "no error shown".
  if (reselected) showToast(t('finding_post') || 'Finding the post…', deadline + 500);
  Promise.resolve(p).catch(function () { }).then(function () {
    var checkedCache = false;
    (function attempt() {
      if (done) return;
      if (selectedChannel !== chNum) { done = true; if (reselected) hideToast(); return; } // user navigated away
      if (scrollToMsg(msgId)) { finish(true); return; }
      if (checkedCache) { setTimeout(attempt, 500); return; }
      checkedCache = true;
      // Cache check, once — the response carries the whole message window,
      // so per-retry fetches would churn megabytes on a phone. Valid once:
      // the oldest cached ID only rises, and a post older than it can never
      // arrive via refresh (bounded server window) — fail now instead of
      // burning the whole retry window.
      fetch('/api/messages/' + chNum).then(function (r) { return r.json(); }).then(function (d) {
        if (done) return;
        var ms = (d && d.messages) || [];
        var oldest = Infinity;
        for (var i = 0; i < ms.length; i++) { var mid = ms[i].ID || ms[i].id || 0; if (mid && mid < oldest) oldest = mid; }
        if (ms.length && +msgId < oldest) { finish(false); return; }
        setTimeout(attempt, 500);
      }).catch(function () { if (!done) setTimeout(attempt, 500); });
    })();
  });
}
// Active jump-to-post highlight. renderMessages re-applies it when a
// background refresh rebuilds the DOM mid-highlight — without this the
// ring vanished after a few ms (or never painted) whenever the refresh
// that selectChannel kicks off landed right after the jump.
var _msgHighlight = null;
function findMsgEl(id) {
  var els = document.querySelectorAll('.msg');
  for (var i = 0; i < els.length; i++) {
    var spans = els[i].querySelectorAll('.msg-meta span');
    for (var j = 0; j < spans.length; j++) {
      // Exact match on the #ID span — a substring test would let #767
      // hit #76737.
      if (spans[j].textContent === '#' + id) return els[i];
    }
  }
  return null;
}
function highlightMsgEl(el) {
  el.classList.remove('msg-highlight');
  void el.offsetWidth; // restart the animation on re-apply
  el.classList.add('msg-highlight');
}
function scrollToMsg(id) {
  var el = findMsgEl(id);
  if (!el) return false;
  // ch guards the re-apply: message IDs are per-channel, so a same-numbered
  // message in another channel must not inherit the highlight.
  _msgHighlight = { id: String(id), ch: selectedChannel, until: Date.now() + 2500 };
  // Instant, not smooth: a long smooth scroll consumes the highlight window
  // before arrival and janks the Android WebView compositor.
  el.scrollIntoView({ block: 'center' });
  highlightMsgEl(el);
  return true;
}
function isPersian(text) { return text && (text.match(/[\u0600-\u06FF]/g) || []).length > text.length * 0.25 }

