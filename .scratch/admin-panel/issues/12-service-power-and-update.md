# 12 Service and power, update and rollback

Status: ready-for-agent
Blocked by: 02, 04, 05
Spec: `.scratch/admin-panel/spec.md`, "Service, power and update", user stories 45 to 57
Design: `.scratch/admin-panel/mockup.html`, the "Service and power" page and the stopped and not-answering banners

## What

- **systemd** behind an interface that runs `systemctl`: restart, stop and start, enable and disable (start on boot), `reboot` and `poweroff`.
- **Answering:** stop and resume through `controlapi`'s `POST /answering`.
- **Service API:**
  - state: running, not answering or stopped, since when, and the last action line;
  - every power action;
  - shutdown refuses unless the typed domain matches.
- **Update:**
  - the release source is a panel setting (owner/repo) and defaults to the installer's repo;
  - the latest tag comes from `internal/update`'s tag lookup and `IsNewer`;
  - the linux/arch asset is downloaded and checked against the release's `SHA256SUMS`, and refused without a match;
  - the previous binary is kept, the new one is swapped in with an atomic rename, and the server restarts;
  - if `/status` isn't healthy within 60 seconds, roll back automatically and say so;
  - manual rollback.
- **Shadowing flags:** detect a `thefeed-server` unit whose `ExecStart` passes flags that shadow env settings, and offer to fix it with a restart.
- Every action is appended to the action record.
- **UI:**
  - the Service and power page file with rows for Answering queries, Process, Start on boot, Version and Machine;
  - in-place confirms, with a red tint for destructive ones and a typed-domain confirm for shutdown;
  - the status block;
  - the stopped and not-answering banner on Board, Server and Control, with Resume or Start.

## Acceptance

`httptest` tests with a fake systemd that records commands, a fake `controlapi` client, a fake release source and a fake clock. They check:

- each action sends the right command;
- shutdown without the typed domain is refused;
- a bad checksum is refused;
- a good update swaps binaries and restarts;
- an unhealthy start rolls back;
- manual rollback;
- shadowing-flag detection.

Plus a manual UI check against the mockup.

## Notes

- Use one page file following ticket 05's registration. Register the API as its own module.
