# 15 Files the panel writes keep their owner

Status: ready-for-agent
Blocked by: none
Spec: `.scratch/admin-panel/spec.md`, section "Running in Docker", "Files keep their owner"; user story 100

## What

- **The writer.** `serverconfig.WriteFile` writes through `CreateTemp` and `Rename`. It keeps the old file's mode but not its owner, so one panel edit leaves `thefeed.env` root-owned 0600. The server container runs as uid 1000 and could no longer read its own config.
- **The fix.** Chown the temp file to the existing file's uid and gid before the rename. When there's no file yet, keep today's behaviour.
- **Every writer.** Apply the same rule to every path that rewrites a file in the server's data directory: the env file, the four lists, and anything else found by grepping for `Rename` and `WriteFile` under `internal/serverconfig` and `internal/admin`.

## Acceptance

- **Seam 3.** A test at `internal/serverconfig`: writing over a file owned by another uid and gid keeps both. Changing a file's owner needs root, so the test skips when not root. The Docker setup of the end-to-end suite (ticket 20) proves it for real.
- A file the panel creates fresh is still 0600.
