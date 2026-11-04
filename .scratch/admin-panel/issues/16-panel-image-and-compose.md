# 16 The panel's image, its settings file, and the compose file

Status: ready-for-agent
Blocked by: none
Spec: `.scratch/admin-panel/spec.md`, section "Running in Docker"; user stories 94 and 99

## What

- **Own settings.** thefeed-admin reads `<data-dir>/thefeed.env` at start. A variable already in the real environment wins. Reuse `serverconfig.ParseEnv`; don't copy cmd/server's loader.
- **`THEFEED_ADMIN_CONTAINER=1`.** The real `systemctl` adapter (`internal/admin/service`) and the real `journalctl` adapter (`internal/admin/logs`) then run through `nsenter -t 1 -m -u -i -n -p --`. They keep the same output and error handling.
- **`reset-admin`'s busy message.** Under `THEFEED_ADMIN_CONTAINER`, it prints the Docker commands:
  1. `docker stop thefeed-admin`
  2. a one-off `docker run --rm -v /opt/thefeed:/opt/thefeed <image> reset-admin`
  3. `docker start thefeed-admin`
- **Panel image.** A multi-stage Dockerfile for thefeed-admin: an alpine runtime plus `util-linux-misc` for `nsenter` and `ca-certificates`.
  - `ENV THEFEED_ADMIN_CONTAINER=1`
  - `ENTRYPOINT ["thefeed-admin"]`
  - default data dir `/opt/thefeed/data`
  - it runs as root

  The existing server Dockerfile stays, apart from what compose needs.
- **CI** (`build.yml`):
  - **Release tags:** build both images for linux/amd64 and linux/arm64 and push them to `ghcr.io/<lowercased owner>/thefeed-server` and `ghcr.io/<lowercased owner>/thefeed-admin`, tagged with the release tag and `latest`.
  - **Pull requests:** build both images and don't push.
  - The workflow needs `packages: write` only where it pushes.
- **`docker-compose.yml`, rewritten as the spec says.**
  - **`server`:** image `ghcr.io/sartoopjj/thefeed-server:${THEFEED_SERVER_TAG:-latest}`, with the image repository overridable through `THEFEED_SERVER_IMAGE`. It gets only `./data:/data` and `5300:5300/udp`: no `env_file`, no `environment`, no settings flags. Restart policy `unless-stopped`.
  - **`admin`:** image the same way (`THEFEED_ADMIN_IMAGE`, `THEFEED_ADMIN_TAG`).
    - `privileged`, `pid: host`, `network_mode: host`, `restart: unless-stopped`
    - environment `THEFEED_ADMIN_SERVER=docker`
    - mounts: `/var/run/docker.sock`, `.:/opt/thefeed`, and `/etc/systemd/system:/etc/systemd/system:ro`
  - **The build-from-source path** stays possible, as a commented `build:` line or an override file. Keep it small.
- **Settings files.** `.env.example` becomes the compose `.env`: image repositories and tags only. A new `configs/thefeed.env.example` is what goes in `data/thefeed.env`. It sets:
  - `THEFEED_DOMAIN`, `THEFEED_KEY` and `THEFEED_NO_TELEGRAM`;
  - `THEFEED_LISTEN=:5300`;
  - `THEFEED_CONTROL_SOCKET=true`.
- **README.** Rewrite the Docker section: the two files, `docker compose up -d`, where the panel is, the setup code (`docker logs thefeed-admin`), and the port 53 redirect as before.

## Acceptance

- `go build ./...`, `go vet ./...` and `go test ./...` are clean.
- The PR's CI builds both images.
- `docker compose config` accepts the compose file.
- Ticket 20 runs the containers for real.
- No new Go dependency.

## Notes

- Ticket 17 adds the Docker backend and reads `THEFEED_ADMIN_SERVER`. This ticket only sets it in compose.
