# 08 Logs from the journal, per feature and for the server

Status: resolved
Blocked by: 04
Spec: `.scratch/admin-panel/spec.md`, section "Logs"

## What

- **Source.** Read the `thefeed-server` unit's journal through `journalctl --output=json` behind an interface: with a since/until cursor, newest first, and a page size.
- **Feature.** Map each line to a feature using one table of the prefixes the server already logs. Grep `internal/server` for the real prefixes, for example `[dns]`, `[admin]`, `x:`, `mtproto:`, `chat:`, `github-relay:`, `dns-media:`, `fetch cycle`, `[dns_hourly]`.
- **Level.** Classify ERROR, WARN or INFO with keyword rules kept in one table.
- **API:**
  - a feature log page with an "all" or "warnings and errors" filter and a cursor;
  - the recent server log.

  Lines are grouped by day in the response.

## Acceptance

- Tests feed recorded `journalctl` JSON fixtures through the interface and check feature mapping for every prefix, level classification, both filters, paging and day grouping.

## Notes

- Register as its own module. No UI here; that's ticket 09.
