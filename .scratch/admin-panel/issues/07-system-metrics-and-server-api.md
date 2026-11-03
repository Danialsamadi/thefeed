# 07 System metrics, reachability and the Server API

Status: ready-for-agent
Blocked by: 02, 04
Spec: `.scratch/admin-panel/spec.md`, section "System metrics and reachability (Linux only)" and the Panel JSON API's Server

## What

- **Metrics from `/proc` and `statfs`, with no new dependencies:**
  - CPU%;
  - RAM total and available;
  - thefeed-server's RSS and open file descriptors (finding the PID through systemd or `/proc`);
  - disk use of the data directory and filesystem;
  - network in and out on the default-route interface;
  - load average.
- **Sampling:** every 10 seconds, kept in memory for an hour, with per-minute averages stored for 7 days.
- **Breakdowns:** RAM is thefeed-server, media cache (from `/status`), system and free. Disk is media cache, reports and logs, system and data, and free.
- **Reachability:**
  - Public resolvers: a configurable list, defaulting to about 10 well-known resolvers, is asked for the server's own feed domain every 15 minutes, and the panel counts the ones that answer.
  - Telegram data centres, Nitter instances and GitHub come from `/status`.
- **Process facts:** running since, load, open sockets, and the listen address with the port-53 redirect.
- **API:**
  - Server metrics for 15 min, 1 h, 24 h and 7 days, with a scale and the average, peak and now values the mockup shows;
  - the pinned CPU, RAM, disk and out values the Board shows.
- On non-Linux hosts the endpoints return "not available", and the panel says so.

## Acceptance

- Tests feed fixture `/proc` files through a filesystem interface and check the parsing, the CPU delta math, the per-minute averaging and retention pruning on a fake clock.
- A resolver probe test uses a fake DNS client.
- The API shapes are covered.

## Notes

- Register as its own module. No UI here; that's ticket 09.
