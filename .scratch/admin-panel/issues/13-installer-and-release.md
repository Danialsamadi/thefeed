# 13 Installer and release workflow

Status: resolved
Blocked by: 01, 04
Spec: `.scratch/admin-panel/spec.md`, sections "Installer" and "Service, power and update" (checksums), plus Further Notes "Fork"

## What

- **`scripts/install.sh`:**
  - Installs `thefeed-admin` and a `thefeed-admin` systemd unit next to `thefeed-server`.
  - Asks for an optional panel hostname.
  - Opens 443/tcp when ufw is active.
  - At the end, prints the panel URL, the setup code and, when self-signed, the certificate fingerprint.
  - Writes every setting to `thefeed.env`. The `thefeed-server` unit keeps only `--data-dir` and reads everything else from the env file.
  - On re-install, migrates an old unit's flags into the env file.
  - `--uninstall` removes the panel too.
  - The release repo the installer used is recorded for the panel's update source.
- **Release workflow (`.github/workflows/build.yml`):** builds `thefeed-admin` for the same Linux targets as the server, and publishes a `SHA256SUMS` file covering every release asset.
- **Makefile, Dockerfile and docker-compose:** build or include `thefeed-admin` where they build the server, if that fits their purpose.

## Acceptance

- Extend the install-script tests. Prior art: `cmd/server/install_defaults_test.go` and `internal/web/install_script_test.go`. Assert that:
  - the unit no longer shadows env settings;
  - the env file gets every registry key;
  - the panel unit is written.
- The workflow YAML has the new build and checksum steps, checked by a test or a dry run.

## Notes

- Ticket 14 runs this installer for real.

## Comments

Contracts other tickets rely on (2026-11-04):

- `thefeed.env` also holds `THEFEED_ADMIN_HOSTNAME` (panel hostname, empty = self-signed) and `THEFEED_ADMIN_RELEASE_REPO` (owner/repo the installer used; ticket 12's default update source). The panel unit reads the file with `EnvironmentFile=-`.
- Release assets: `thefeed-admin-linux-{amd64,arm64}`, plus `SHA256SUMS` with `sha256sum` lines (`<hex>  <name>`, no directory) over every asset.
- A fork installs its own releases with `curl ... | sudo GITHUB_REPO=owner/repo bash`.
- The settings table in `install.sh` must match `internal/serverconfig`: a new registry entry (e.g. 02's `THEFEED_CONTROL_SOCKET`) needs one row there, or `TestInstallScriptSettingsMatchRegistry` fails.
- Not done: `.gitlab-ci.yml` doesn't build the panel (not in this ticket), so installs from the GitLab mirror skip it with a warning. Docker isn't changed: the panel needs systemd, the journal and root.
- A run without `GITHUB_REPO` reuses the recorded `THEFEED_ADMIN_RELEASE_REPO`.
- Rollback floor: servers before v0.40.1-rc0 don't read `THEFEED_NO_TELEGRAM`/`THEFEED_LISTEN` from env, so `--version` older than that runs badly under the flagless unit (the old unit is in `thefeed-server.service.bak` when one was migrated).
- Ticket 14: install.sh has no way to install locally built binaries yet; 14 adds it.
