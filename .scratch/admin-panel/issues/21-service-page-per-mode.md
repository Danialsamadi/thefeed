# 21 The Service page per mode

Status: ready-for-agent
Blocked by: 09, 17
Spec: `.scratch/admin-panel/spec.md`, section "Running in Docker"; user story 101

## What

The Control > Service and power page reads ticket 17's `GET /api/service` fields:
- **Name.** It says "Container thefeed-server" or "Service thefeed-server".
- **Boot.** The start-on-boot switch reads "Start with Docker" in Docker mode.
- **Shadowing.** In Docker mode, the shadowing notice lists the compose settings to remove, with no fix-unit button.
- **Unavailable actions.** An action the mode doesn't offer isn't drawn. A disabled control is not enough.
- **Copy.** Follow the mockup's tone and the existing page's patterns, and the antislop rules the earlier UI tickets used.

## Acceptance

- Node unit tests cover any new pure helper.
- The page is checked in the browser against a panel run with a fake Docker mode, such as the dev server the UI tickets used, in both modes, at phone and desktop widths, dark and light.
