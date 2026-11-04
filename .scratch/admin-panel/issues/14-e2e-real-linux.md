# 14 End-to-end suite on real Linux with systemd

Status: resolved
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

## Comments

What's in place (2026-11-04):

- **`install.sh --local <dir>`** installs `<dir>/thefeed-{server,admin}-linux-<arch>` instead of downloading. It skips `install_base`, and takes the version from the binary's `--version`. The `/dev/tty` reopen is now `reopen_tty`, so the sourced harness can stub it. `TestInstallFromLocalBinaries` runs the whole `install_thefeed` that way, over `e2e/testdata/thefeed.env` with an empty stdin.
- **Suite:** `e2e/e2e_test.go` (`//go:build e2e`). It's one `TestE2E`, with the ticket's flows as subtests in order; the first failure stops the run.
  - **Setup:** it seeds `/opt/thefeed/data/thefeed.env` from `e2e/testdata/thefeed.env`, plus `THEFEED_ADMIN_RELEASE_BASE_URL`. Every prompt then has a default, so install.sh runs with no answers. It runs install.sh with `Setsid`, so there's no `/dev/tty`. DNS listens on `127.0.0.1:5300`; the panel is on `:443`, self-signed.
  - **Checks:** the TLS client accepts only the panel's own `tls/cert.pem`.
  - **Answering:** besides the ticket, it also checks the spec's "stop answering survives a panel restart".
  - **Cleanup:** it refuses to run if `/opt/thefeed` or either unit exists. On failure it dumps the journal. At the end it runs `--uninstall`, then `rm -rf /opt/thefeed`.
- **Fake release:** `e2e/release.go`. It's untagged, and `TestFakeReleaseReadsLikeGitHub` runs it through the real `update.LatestTag`/`OpenAsset` in plain `go test`.
- **Running it:** `make e2e`.
  - It builds as you, at fixed versions: v0.98.0 installed, v0.99.0 in the fake release. `git describe` isn't used, because CI checkouts have no tags.
  - Then it runs `sudo ../build/e2e/e2e.test` from `e2e/`.
  - CI: `.github/workflows/e2e.yml`, on `pull_request` and `workflow_dispatch`. `build.yml` is untouched.
- **Never run:** the suite itself, here or anywhere. This Mac has no systemd. It compiles (`go vet -tags e2e`, `GOOS=linux` too), and the install path was dry-run through the harness.
