# 03 Reader health detail and per-feature checks

Status: resolved
Blocked by: 02
Spec: `.scratch/admin-panel/spec.md`, the control endpoint's "Reader changes" and `POST checks/{feature}`

## What

- **Fetch cycles.** The Telegram reader and the public (no-login) channel reader each keep the last 10 fetch cycles: start, duration, channels fetched of total, posts, interval. These go in `/status` under `channels`.
- **X.** The X reader keeps the last result per Nitter instance (host, ok or error, error text, latency, time) plus account count and last post time. These go in `/status` under `x`.
- **Telegram over DNS.** The relay keeps per data centre: last dial duration, failures in the last hour, last error, last time. These go in `/status` under `mtproto.dcs`.
- **`POST /checks/{feature}`** returns `{ok, summary, detail}` and records into the same stats:
  - `x`: probe every configured instance now;
  - `mtproto`: dial every data centre;
  - `ghRelay`: the relay's recovery probe;
  - `chat`: store and read back a test message, then delete it;
  - `channels`: run a fetch cycle and return its summary.
- **Log lines.** These checks log one line each with the feature's existing prefix, because ticket 08 reads features from the journal by prefix.

## Acceptance

- An in-process server with `httptest` upstreams covers:
  - an X check against one healthy and two failing fake Nitter instances reports per-instance results;
  - a GitHub relay check against a 403 fake reports token denied.

  Prior art: `internal/server/github_relay_test.go`.
- Fetch-cycle and per-data-centre stats appear in `/status` after a cycle or dial.
- The chat check leaves no test message behind.

## Notes

- Keep additions small and inside each reader. The status endpoint only reads snapshot methods.
