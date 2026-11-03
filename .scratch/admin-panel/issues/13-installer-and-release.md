# 13 Installer and release workflow

Status: ready-for-agent
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
