# 14 End-to-end suite on real Linux with systemd

Status: ready-for-agent
Blocked by: 09, 11, 12, 13
Spec: `.scratch/admin-panel/spec.md`, section "End-to-end on real Linux (opt-in)"

## What

- **Opt-in.** An e2e suite behind a build tag or Make target, so it isn't part of plain `go test`. It runs on a disposable Linux host with real systemd and journal:
  - in CI, a GitHub Actions Ubuntu runner job, which has systemd and sudo;
  - locally, a documented systemd-enabled container or VM.
- **Setup.** It installs locally built binaries with `install.sh`, using the self-signed certificate.
- **Flows**, driven through the panel's HTTPS API:
  1. **First run:** set up the admin with the code read from the journal, then log in.
  2. **Restart:** change a restart-only setting, see it waiting, restart, then confirm the server's PID changed and the setting is running in `/status`.
  3. **Public channel:** add one, and see it live without a restart.
  4. **Answering:** stop answering and see a real DNS query go unanswered, then resume.
  5. **Stop and start:** both work, and the Board shows the change.
  6. **Update:** update to a release served from a local fake source, with `SHA256SUMS`, then roll back.
  7. **Lock:** login lockout works end to end.
- Reboot and shutdown are not exercised.

## Acceptance

- The CI job is green on the PR branch, or it's runnable on demand with the local instructions documented in its README section.
