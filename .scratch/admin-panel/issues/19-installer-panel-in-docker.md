# 19 install.sh runs the panel in Docker

Status: ready-for-agent
Blocked by: 16
Spec: `.scratch/admin-panel/spec.md`, sections "Installer" and "Running in Docker"; user story 95

## What

- **The question.** "Run the admin panel in Docker? [Y/n]" goes next to the panel hostname question. The default is the saved `THEFEED_ADMIN_DOCKER`, or Yes. The answer is written to `thefeed.env` with the other settings, and added to `SERVER_SETTINGS` if `TestInstallScriptSettingsMatchRegistry` needs it.
- **Yes:**
  - **Docker.** When Docker is missing, install it from the OS packages: `docker.io` on apt, and the distro's package on dnf/yum, or say what to run when there's none. Enable and start it.
  - **Image.** Pull `ghcr.io/<lowercased owner of GITHUB_REPO>/thefeed-admin:<version>`.
  - **`--local <dir>`.** Load `<dir>/thefeed-admin-image-linux-<arch>.tar` instead, and say so. `make e2e` builds that archive.
  - **The old unit.** Remove the systemd panel unit if one exists.
  - **The container.** Recreate it as `thefeed-admin` with the settings in the spec: `--privileged --pid host --network host --restart unless-stopped`, mounts `/var/run/docker.sock`, `/opt/thefeed` and `/etc/systemd/system`, and `THEFEED_ADMIN_SERVER=systemd`.
- **No:** today's unit, and remove a `thefeed-admin` container if one exists.
- **`show_panel`.** Read the "admin: serving" and setup-code lines from `docker logs thefeed-admin` when it's a container. Print the reset command that ticket 16 defines.
- **`--uninstall`.** Remove the container and the image too. Keep the data, as today.
- **ufw.** Open 443/tcp in both modes, as today.

## Acceptance

- The install.sh harness tests (`scripts` tests run from `go test`) cover:
  - the question's default with and without the saved setting;
  - the Docker install command chosen per package manager, with stubs for `docker`, `apt-get` and `systemctl`;
  - the `docker run` arguments;
  - `--local` loading the archive;
  - uninstall removing the container.
- The end-to-end suite (ticket 20) runs it for real.
