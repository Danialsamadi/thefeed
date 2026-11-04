// Control, This panel, Appearance: dark or light, kept in this browser.
'use strict';
(() => {
  const S = Panel.S, KEY = 'thefeed-admin-theme';
  try { if (localStorage.getItem(KEY) === 'light') S.theme = 'light'; } catch { /* storage off: dark */ }

  Panel.section({
    group: 'This panel', id: 'look', title: 'Appearance',
    summary: () => (S.theme === 'dark' ? 'Dark' : 'Light'),
    render: () => Panel.sec('Theme', `<div class="it"><div class="tx"><b id="look-theme">Theme</b><small>The app's own dark and light colors</small></div>
      <div class="seg inl" role="group" aria-labelledby="look-theme">${[['dark', 'Dark'], ['light', 'Light']].map(([t, label]) =>
        `<button type="button" data-act="theme" data-t="${t}" data-k="theme-${t}" aria-pressed="${S.theme === t}">${label}</button>`).join('')}</div></div>`),
  });

  Panel.acts.theme = b => {
    S.theme = b.dataset.t;
    try { localStorage.setItem(KEY, S.theme); } catch { /* kept for this visit only */ }
    Panel.render();
  };
})();
