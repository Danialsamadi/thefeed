# 18 Logs from a thefeed-server container

Status: ready-for-agent
Blocked by: 17
Spec: `.scratch/admin-panel/spec.md`, section "Running in Docker", "Logs"; user story 97

## What

- **Source.** With `THEFEED_ADMIN_SERVER=docker`, the logs module reads the server container's log through ticket 17's Engine client: `GET /containers/{name}/logs?stdout=1&stderr=1&timestamps=1`, with `tail` and `until` for paging.
  - Split the 8-byte multiplexed frames when the container has no TTY. Read raw lines when it has one.
  - Take the timestamp prefix as the entry's time.
- **Same API.** The `/api/logs/server` and `/api/logs/{feature}` contracts stay the same. The cursor in Docker mode is an opaque string built from a timestamp, and the next page is entries older than the last one shown. Feature matching and levels use the existing tables, unchanged.
- **The journal path** is unchanged.

## Acceptance

Seam 6 tests, through the logs API:
- frames from stdout and stderr interleaved;
- a line split across two frames;
- paging with the cursor;
- the feature filter and the bad-only filter;
- the Engine being unreachable, which gives the existing error answer.
