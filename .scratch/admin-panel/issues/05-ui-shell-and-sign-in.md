# 05 Panel UI shell, sign-in screens, Account and Appearance

Status: resolved
Blocked by: 04
Spec: `.scratch/admin-panel/spec.md`, the Solution, "Shape" (no build step, embedded) and user stories 1 to 15, 21, 25, 26 and 87 to 92
Design: `.scratch/admin-panel/mockup.html`, the approved mockup. Copy its tokens, layout, copy and states; don't redesign.

## What

- **Embedding.** Static UI embedded in `thefeed-admin` (HTML, CSS, vanilla JS, no build step), the same approach as the client web UI.
- **Look:**
  - thefeed's dark and light tokens, including the mockup's light status colors (`#276a2b` and `#8a4f00`) and the `--offbar` token;
  - Vazirmatn bundled locally, copied from the client's static assets;
  - icons from the client icon set;
  - nothing loaded from third-party sites.
- **Shell:**
  - nav with Board, Server and Control: a floating bottom bar on a phone, an icon rail from 640 px, a labelled rail from 980 px;
  - list and detail panes using named container queries (`app`, `pane`);
  - the back arrow only in one-pane layouts;
  - Escape closes the open detail;
  - focus is kept across re-renders.
- **Pages are pluggable.** Each page lives in its own JS file and registers itself, so tickets 09, 11 and 12 each add a file plus one script tag.
- **Sign-in screens** are wired to 04's API:
  - first run (setup code, username, password twice, Show, inline errors, values kept between errors);
  - login (form error, tries-left countdown, lockout disables the form);
  - logged-out message.
- **Control:** Control's section list with the "This panel" group, plus:
  - Account: signed in as, since when, Log out, Change password, the session list;
  - Appearance: dark or light, stored per browser.
- **Shared states:**
  - "Can't reach the server" banner;
  - first-load state;
  - pause or resume for live updates, honouring reduced motion.
- **JS tests.** Pure helpers (formatting, focus key and the like) get zero-dependency node tests run from `go test` and skipped without node. Prior art: `internal/web/jstest` and `js_links_test.go`.

## Acceptance

- `go test` serves the shell and its assets from the embedded files.
- Node tests for the helpers pass.
- Manual check in a browser:
  - the flows match the mockup at 360, 700 and 1140 px in both themes;
  - all text passes WCAG AA, measured the way the antislop follow-up did it.

## Notes

- Board, Server, feature pages and the other Control sections come in later tickets. Leave their nav entries showing an empty state.
