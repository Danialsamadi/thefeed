# 20 The end-to-end suite in three setups

Status: ready-for-agent
Blocked by: 15, 16, 17, 18, 19
Spec: `.scratch/admin-panel/spec.md`, section "End-to-end on real Linux (opt-in)", its three setups

## What

The existing suite (`e2e/`, `make e2e`, `.github/workflows/e2e.yml`) runs its seven flows in three setups, as a matrix of CI jobs. A setting such as `E2E_SETUP` picks the setup.

1. **`script-docker`** (new default): `install.sh --local` with the panel in Docker, which is the empty-stdin answer. `make e2e` also builds the panel image archive (`docker build` plus `docker save`) next to the binaries.
2. **`script-systemd`:** the same with `THEFEED_ADMIN_DOCKER=0` seeded in `thefeed.env`. This is today's run.
3. **`compose`:**
   - Start `registry:2` on `localhost:5000`. Build the server image at v0.98.0 and v0.99.0 and the panel image, and push them there.
   - Write `.env` (repositories on `localhost:5000`, tag v0.98.0) and `data/thefeed.env` from testdata, plus a channel.
   - Run `docker compose up -d` in a temp directory holding the repo's `docker-compose.yml`.
   - Run the same flows. These parts differ from the script setups:
     - **PID:** from `docker inspect`.
     - **Setup code:** from `docker logs`.
     - **Update:** the fake GitHub release still answers the latest tag, and the panel pulls `localhost:5000/thefeed-server:v0.99.0`. Check that `.env` now holds v0.99.0, and that after a rollback it holds v0.98.0 again.
     - **Ownership:** check `thefeed.env` is still owned by uid 1000 after the restart flow's edit (ticket 15).
   - Clean up: `docker compose down -v`, and remove the registry and images.
- **Preflight and cleanup** refuse to run over an existing install and remove everything afterwards, in every setup.

## Acceptance

All three jobs are green on the PR.
