# 09 Board, feature status pages and Server UI

Status: resolved
Blocked by: 05, 06, 07, 08
Spec: `.scratch/admin-panel/spec.md`, user stories 16 to 44
Design: `.scratch/admin-panel/mockup.html`, the Board status list, feature status page and Server page

## What

- **Board** page file:
  - pinned CPU, RAM, disk and out, which opens Server;
  - groups headed by count;
  - status rows with state, fact, 60 bars and %;
  - the bar legend;
  - polling the Board summary every 5 seconds unless paused;
  - the attention count on the nav.
- **Feature status page:**
  - status block with actions, where Check now shows "Checking…" and then the result;
  - right-now facts;
  - uptime with axis, counts and the `--offbar` color;
  - the dependency table, and for X the Nitter instance editor. The editor saves through ticket 10's API, so leave a hook if 10 isn't merged yet;
  - incidents table or empty state;
  - the log with the "All lines / Warnings and errors" filter.
- **Server** page:
  - range buttons, CPU and network charts with a scale;
  - RAM and disk stacks with legends;
  - reachability, process, and the recent log.
- **Shared states:** Off and Stopped wording, and the unreachable and first-load states.
- **JS tests** for the pure helpers: bars and %, axis labels, log grouping.

## Acceptance

- Node tests pass.
- Manual check against the mockup at 360, 700 and 1140 px in both themes: layout, copy, states, focus moving to Back and returning to the row, and WCAG AA contrast including the bars' 3:1.

## Notes

- Use one page file per page, following ticket 05's registration.
