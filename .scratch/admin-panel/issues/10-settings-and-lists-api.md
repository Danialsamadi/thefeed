# 10 Settings, waiting changes and source lists API

Status: ready-for-agent
Blocked by: 01, 02, 04
Spec: `.scratch/admin-panel/spec.md`, "Configuration: one source of truth" and the Panel JSON API's Settings, Lists and Domains

## What

- **Settings:**
  - Read every registry setting with its saved value (from `thefeed.env`), its running value (from `/status`'s effective config) and a waiting flag.
  - Patch one setting: validate it through the registry, then write with the "changed elsewhere" guard.
  - Discard all: rewrite the files to the running config.
  - Secrets are never returned. The GitHub token gets a replace-only endpoint that checks the format (`ghp_` or `github_pat_`).
- **Lists:** add, rename, move and remove for public channels, private invite links, X accounts and Nitter instances, using 01's codecs.
  - An invite link pasted as a channel is filed under private channels.
  - The last Nitter instance can't be removed.
  - Private additions are refused while the Telegram login is logged out.
  - Public channel changes write `channels.txt` and call `/channels/reload`, so they apply live. Everything else waits for a restart.
  - Each response returns the new list, whether it applied live, and the reader channel numbers.
- **Telegram logout:** set `THEFEED_NO_TELEGRAM`, delete the session file, and return "restart needed". Ticket 12 owns the restart.
- **Domains:** list them with what each serves, plus an NS check that asks public resolvers for the NS records of each domain and reports whether they point here.
- Every change is appended to the action record.

## Acceptance

Tests use a temporary data directory, a fake `controlapi` client and a fake clock. They check:

- an out-of-range patch is refused, and the file is byte-identical;
- a valid patch changes only that line;
- a setting shows waiting until the fake status reports it;
- the "changed elsewhere" refusal works;
- discard restores the files;
- every list operation, including the edge cases above;
- adding a public channel calls reload;
- the token is never echoed.

## Notes

- Register as its own module. No UI here; that's ticket 11.
