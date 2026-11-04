# 11 Control UI: sources, features and server settings

Status: resolved
Blocked by: 05, 10
Spec: `.scratch/admin-panel/spec.md`, user stories 58 to 86
Design: `.scratch/admin-panel/mockup.html`, Control's Telegram channels, X accounts, Messenger, Telegram over DNS, Media relays, Fetching and DNS and domains pages

## What

- **Control section list:**
  - one page file each for the sections above;
  - each section's summary line;
  - a "N changes wait for a restart" line;
  - the shared waiting-changes notice with Discard and Restart. Restart opens ticket 12's Service page in its confirm state, so leave a hook if 12 isn't merged yet.
- **List pages:**
  - add forms with inline validation;
  - search on channels;
  - expandable rows with Rename, Move up, Move down and Remove plus confirm;
  - the reader channel numbers;
  - truncated invite links;
  - the "X is down" callout linking to X's status page;
  - Fetch all now.
- **Settings pages:**
  - switches, number fields that commit on change with in-place patching (as the mockup does), segmented options, text fields and the token replace form;
  - "applies after restart" on changed fields;
  - the Telegram login row with Log out and its confirm;
  - the domain table with Check NS records.

## Acceptance

- JS tests for the pure helpers (summaries, list validation messages) pass.
- Manual check of every flow in the mockup's Control click-through against the real API, at phone and wide widths in both themes, with WCAG AA contrast.

## Notes

- Use one page file per section, following ticket 05's registration.
