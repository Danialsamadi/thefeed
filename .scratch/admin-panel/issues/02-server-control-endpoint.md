# 02 thefeed-server control endpoint: status, reload, refresh, answering

Status: ready-for-agent
Blocked by: 01
Spec: `.scratch/admin-panel/spec.md`, section "thefeed-server's local control endpoint (new)"

## What

- **The socket.** `thefeed-server` serves HTTP over a Unix socket in its data directory (`control.sock`, mode 0600).
  - It's on by default; `THEFEED_CONTROL_SOCKET=0` (registered in 01's registry) turns it off.
  - A stale socket file is removed on start, and the socket is closed on shutdown.
- **A shared package (suggested `internal/controlapi`)** holding:
  - the request and response JSON types;
  - a Go client that dials the socket. `thefeed-admin` will use it, so keep the client behind a small interface its tests can fake.
- **Endpoints in this ticket:**
  - `GET /status`:
    - `server`: version, started time, `answering`, and the effective config. That's every registry key with its running value; secrets are left out, using the registry's secret flag.
    - `feed`: per-domain query counters since start, plus a per-minute rate over the last minute. Reuse the counters behind the hourly report.
    - `chat`: active addresses today, messages today, waiting, inbox-full rejections today, store errors in the last 10 minutes. Add counters where missing.
    - `mediaCache`: the existing `MediaCacheStats`.
    - `ghRelay`: the existing `RelayStatus`.
    - `mtproto`: tunnels open and cap, from the existing `Stats()`.
  - `POST /channels/reload`: re-read `channels.txt` and apply it through the same path the DNS admin add and remove commands use (`SetChannels`, `UpdateChannels`, `RequestRefresh`, X base channel).
  - `POST /refresh`: the same as the DNS admin refresh.
  - `POST /answering` with `{"on": bool}`:
    - **Off:** the DNS handler sends no response to any query, and open Telegram-over-DNS tunnels are closed.
    - **On:** answering resumes.
    - It isn't persisted; it's always on after a start.

## Acceptance

Tests use an in-process server on `t.TempDir()`. Prior art: `internal/server/dns_multidomain_test.go` and `media_integration_test.go`.

- `/status` has the documented shape, and no secret appears anywhere in it.
- Editing `channels.txt` then calling `/channels/reload` changes the served channel list without a restart.
- With answering off, a real DNS query to the listener gets no response. After answering is back on, it's answered again.
- The client round-trips every endpoint against the real handler.

## Notes

- Ticket 03 adds per-reader detail and checks to this same endpoint. Leave room in `/status` for `channels`, `x` and per-data-centre `mtproto` fields.
