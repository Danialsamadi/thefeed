// a11y.js — cross-cutting accessibility behaviours that don't belong to any one
// feature module:
//
//   1. Keyboard activation for custom (non-native) buttons: any element with
//      role="button" that isn't a real <button>/<a> is activated by Enter/Space,
//      matching native button semantics (WCAG 2.1.1).
//   2. Modal dialog semantics + focus management for the shared .modal-overlay
//      pattern: role="dialog"/aria-modal, derived aria-labelledby, focus moves
//      into the dialog on open, Tab is trapped inside it, and focus returns to
//      the trigger on close (WCAG 4.1.2, 2.4.3, 2.1.2).
//
// Pure progressive enhancement — everything still works (just less accessibly)
// if this file fails to load.
(function () {
  'use strict';

  // ---- 1. Keyboard activation for custom buttons --------------------------
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Enter' && e.key !== ' ' && e.key !== 'Spacebar') return;
    var el = e.target;
    if (!el || el.getAttribute('role') !== 'button') return;
    var tag = el.tagName;
    if (tag === 'BUTTON' || tag === 'A' || tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
    e.preventDefault();           // stop Space from scrolling the page
    el.click();
  });

  // ---- 2. Modal dialog semantics + focus management -----------------------
  var FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),' +
    'select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';
  var lastTrigger = null; // element to restore focus to when the dialog closes
  var labelSeq = 0;

  function dialogEl(overlay) {
    return overlay.querySelector('.modal') || overlay;
  }

  function visibleFocusables(container) {
    return Array.prototype.filter.call(
      container.querySelectorAll(FOCUSABLE),
      function (el) { return el.offsetParent !== null || el === document.activeElement; }
    );
  }

  // Apply static dialog semantics once per overlay.
  function ensureDialogSemantics(overlay) {
    var dlg = dialogEl(overlay);
    if (dlg.getAttribute('role') !== 'dialog') dlg.setAttribute('role', 'dialog');
    if (!dlg.hasAttribute('aria-modal')) dlg.setAttribute('aria-modal', 'true');
    // Derive an accessible name from the first heading if none is set.
    if (!dlg.getAttribute('aria-label') && !dlg.getAttribute('aria-labelledby')) {
      var h = dlg.querySelector('h1,h2,h3');
      if (h) {
        if (!h.id) h.id = 'a11y-dlg-title-' + (++labelSeq);
        dlg.setAttribute('aria-labelledby', h.id);
      }
    }
  }

  function onOpen(overlay) {
    ensureDialogSemantics(overlay);
    lastTrigger = document.activeElement;
    // Move focus into the dialog, unless something inside already has it
    // (some modals focus their own primary input). Defer so the modal's own
    // open handler runs first.
    setTimeout(function () {
      if (!overlay.classList.contains('active')) return;
      if (overlay.contains(document.activeElement)) return;
      var f = visibleFocusables(dialogEl(overlay));
      if (f.length) { try { f[0].focus(); } catch (e) {} }
    }, 40);
  }

  function onClose() {
    if (lastTrigger && typeof lastTrigger.focus === 'function' &&
        document.contains(lastTrigger)) {
      try { lastTrigger.focus(); } catch (e) {}
    }
    lastTrigger = null;
  }

  // Trap Tab within the top-most open overlay.
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Tab') return;
    var open = document.querySelector('.modal-overlay.active');
    if (!open) return;
    var f = visibleFocusables(dialogEl(open));
    if (!f.length) return;
    var first = f[0], last = f[f.length - 1];
    var active = document.activeElement;
    if (e.shiftKey) {
      if (active === first || !open.contains(active)) { e.preventDefault(); last.focus(); }
    } else {
      if (active === last || !open.contains(active)) { e.preventDefault(); first.focus(); }
    }
  });

  // Watch .active toggling on overlays (settings/profiles/etc. add/remove it;
  // core.js builds overlays with className "modal-overlay active").
  function watch(overlay) {
    if (overlay.__a11yWatched) return;
    overlay.__a11yWatched = true;
    var wasActive = overlay.classList.contains('active');
    if (wasActive) onOpen(overlay);
    new MutationObserver(function () {
      var isActive = overlay.classList.contains('active');
      if (isActive === wasActive) return;
      wasActive = isActive;
      if (isActive) onOpen(overlay); else onClose();
    }).observe(overlay, { attributes: true, attributeFilter: ['class'] });
  }

  function scan(root) {
    var nodes = (root || document).querySelectorAll ? (root || document).querySelectorAll('.modal-overlay') : [];
    Array.prototype.forEach.call(nodes, watch);
  }

  function init() {
    scan(document);
    // Catch overlays created later (core.js dialogs, dynamic modals).
    new MutationObserver(function (muts) {
      muts.forEach(function (m) {
        Array.prototype.forEach.call(m.addedNodes, function (n) {
          if (n.nodeType !== 1) return;
          if (n.classList && n.classList.contains('modal-overlay')) watch(n);
          if (n.querySelectorAll) scan(n);
        });
      });
    }).observe(document.body, { childList: true, subtree: true });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
