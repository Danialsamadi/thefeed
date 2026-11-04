# 01 Shared settings registry, env file editor and list codecs

Status: resolved
Blocked by: none
Spec: `.scratch/admin-panel/spec.md`, section "Configuration: one source of truth"

## What

A new shared package that both `thefeed-server` and `thefeed-admin` use to describe, validate, read and write the server's configuration.

- **Settings registry.** One entry per server setting:
  - fields: key, env name, flag name, type (int, bool, string, enum, list), min/max or allowed values, default, Control page, applies (`live` or `restart`), secret (bool);
  - it covers every flag in the server's command (`cmd/server/main.go`) that has an env equivalent, which today is all of them except the one-shot ones (`--login-only`, `--print-*`, `--report*`, `--version`);
  - `Validate(key, value)` returns a field-level error message in the spec's wording, for example "Use a whole number from 1 to 1,000.".
- **Env file editor.**
  - Parse `thefeed.env` into an ordered document that keeps comments, blank lines, order and unknown keys.
  - Get and set by key, then serialize back byte-identical except for the changed lines.
  - Atomic write: temp file, fsync, rename, keeping the file mode. The write takes the hash it expects on disk and refuses with a typed "changed elsewhere" error if the file differs.
- **List codecs** for `channels.txt` (`@name` per line), `x_accounts.txt` (a name per line), `private_channels.txt` (invite links) and the comma-separated Nitter instances setting. Each:
  - keeps its header comment;
  - validates entries: channel names are 5 to 32 characters, starting with a letter; X handles are 1 to 15; instance hosts are a hostname or URL normalised to a host;
  - refuses case-insensitive duplicates.
- **Invite links.** Move or share the server's existing invite-link parser (`ParseInviteHash` and its helpers) so the server and panel use the same code. The server keeps calling it.
- **Server uses the registry.** After flag and env parsing, the server validates its effective values through the registry and refuses to start with a clear message, so the two binaries can't disagree.

## Acceptance

- Table tests show the registry and the server's flags agree on every name and default. Prior art: `cmd/server/install_defaults_test.go`.
- An env file with comments and unknown keys round-trips byte-identical, and editing one key changes only that line.
- The "changed elsewhere" refusal works.
- Each list codec accepts and rejects the spec's examples, and a round trip keeps the header.
- `go build ./...`, `go vet ./...` and the touched packages' tests pass.

## Notes

- Put it in its own package (`internal/serverconfig` is suggested). Don't touch `internal/admin`; that's ticket 04.
- `cmd/server` keeps its flags. Only add the registry lookups and validation.
