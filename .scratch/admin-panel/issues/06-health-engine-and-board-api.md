# 06 Health engine, uptime, incidents, Board and feature APIs

Status: ready-for-agent
Blocked by: 03, 04
Spec: `.scratch/admin-panel/spec.md`, sections "Health, uptime and incidents" and the Panel JSON API's Board, Feature and Checks

## What

- **Sampler.** A sampler in `thefeed-admin` reads the server's `/status` through the `controlapi` client every 60 seconds, and again right after a check.
- **Feature states.** It gives each feature ok, slow, down or off with the spec's rules. Thresholds live in one table.
  - The DNS feed self-test is a real query to the local listener, using the server key read from `thefeed.env`.
  - When the server is stopped or not answering, every feature is down, worded "Stopped" or "Not answering".
- **History:**
  - per-minute samples for 7 days;
  - daily rollups (off, down, slow, up) for 90 days;
  - uptime % = days not down / days not off;
  - incidents open after 2 non-ok samples in a row and close after 2 ok ones; each stores start, end, worst state and summary.
- **APIs:**
  - **Board summary:** every feature's state, word, one-line fact, 60 daily bars and %, attention count and server state.
  - **Feature detail:** status, impact, actions, four facts, uptime with counts, dependency rows, incidents, and the last and next check.
  - **Run a check:** proxies to `POST /checks/{feature}` and returns the result.
- The facts, dependency rows and one-line wording follow the mockup's `FEATURES` data, with real values.

## Acceptance

Tests use a fake `controlapi` client feeding status sequences and a fake clock. They check:

- every rule's ok, slow, down and off transitions;
- a stopped server overrides every feature;
- rollups across midnight;
- uptime % matches the mockup's rule (X: 37 up days of 60 is 61.7%);
- incident open and close;
- the Board and feature JSON shapes.

## Notes

- Register as its own module, following ticket 04's wiring. No UI here; that's ticket 09.
