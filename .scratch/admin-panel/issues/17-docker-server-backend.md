# 17 Service and power for a thefeed-server container

Status: ready-for-agent
Blocked by: none
Spec: `.scratch/admin-panel/spec.md`, section "Running in Docker", items "Docker Engine API" and "thefeed-server as a container"; user stories 96, 98 and 99

## What

- **Engine client.** A small client package for the Docker Engine API: `net/http` with a unix-socket dialer on `/var/run/docker.sock`, API version pinned in the path. Only the calls the spec lists, and no Docker SDK.
  - Ticket 18 reuses it for logs. The log call, with frame splitting, can live here or there.
- **Service module.** With `THEFEED_ADMIN_SERVER=docker`, the container named by `THEFEED_ADMIN_SERVER_CONTAINER` (default `thefeed-server`) backs:
  - **State:** running or stopped, and started from `State.StartedAt`. Paused (not answering) still comes from the control socket.
  - **Restart, stop and start** act on the container.
  - **boot-on and boot-off** set the restart policy through `POST /containers/{id}/update`: `unless-stopped` or `no`. `Boot` reads it back.
  - **Reboot and shutdown** are unchanged: `systemctl` on the host, which ticket 16 runs through `nsenter`.
  - **Shadowing** lists the container's `Config.Env` keys and `Cmd`/`Args` settings flags that beat `thefeed.env`, with an answer telling the operator to remove them from `docker-compose.yml`. `fix-unit` answers 400 with that message.
  - **Update:**
    1. The tag lookup is as now.
    2. The image is the current `Config.Image` repository with the new tag. Pull it.
    3. Write `THEFEED_SERVER_TAG=<tag>` into the compose project's `.env`. That's `THEFEED_ADMIN_COMPOSE_ENV`, default `/opt/thefeed/.env`. Update the key in place and keep every other line, owner and mode.
    4. Recreate the container with the same `Config` (new `Image`, no `Hostname`), `HostConfig` and network endpoints (aliases kept, no IDs or addresses), under the same name.
    5. Start it and apply the existing 60-second health gate.
    6. On failure, recreate with the old tag, write it back, and say so.
  - **Rollback** recreates with `saved.Previous`'s tag and writes it to `.env`.
  - **The version floor** (`v0.40.1-rc0`) and the tag regexp still apply.
- **`GET /api/service`** gains `"server": "systemd" | "docker"`, plus whatever the page needs to show only the actions that work (ticket 21 reads it). Keep it minimal, such as `"fixUnit": false` or an `actions` list. Pick one and document it in the package doc.
- **The systemd path** behaves exactly as now, and its tests stay as they are.

## Acceptance

Tests at seam 6: the real client against an `httptest` server on a unix socket that plays the Engine API, driven through the panel's JSON API (seam 1). They cover:
- state;
- restart, stop and start;
- boot on and off;
- shadowing;
- an update that passes the health gate: `.env` has the new tag, and the container was recreated with the same config and the new image;
- an update that fails it and rolls back;
- rollback;
- the Engine answering an error (502 with its message).

## Notes

- Ticket 21 changes the UI. Here: backend and API only.
