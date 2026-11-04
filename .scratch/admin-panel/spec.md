# Spec: thefeed admin panel

Status: ready-for-agent

## Problem Statement

I run one thefeed server for my readers, and I'm the only person who looks after it. Today every day-2 task means an SSH session:

- I edit `thefeed.env`, `channels.txt`, `x_accounts.txt` and `private_channels.txt` by hand.
- I run `systemctl` and `journalctl`.
- I read `--report` in a terminal.

There's no way to glance at my phone and know whether the server is healthy. Problems go unnoticed. All three Nitter instances can stop answering and X posts silently stop updating for weeks, and nothing tells me.

I have no history either. I can't see uptime per feature, past incidents, or how CPU, RAM, disk and network have been over the last day or week.

Changing anything is error-prone:

- I have to remember which settings the server picks up immediately and which need a restart.
- A typo in a list file or a bad number in the env file only shows up as a failed start.
- Restarting, stopping, updating or rolling back means remembering the right commands.

## Solution

A web admin panel for one operator and one server. It runs on the same machine as a small companion service, separate from thefeed-server, so it keeps working while the server is stopped, restarting or being updated.

The panel runs as a Docker container, or as a systemd service on a server without Docker. Either way it manages thefeed-server, whether the install script set that up as a systemd service or docker compose runs it as a container.

The panel follows the approved mockup, "panel-v3" (see Further Notes):

- **Sign-in:** one admin account, stored in the panel's own database. First run asks for a one-time setup code that the installer prints. There's a login screen, log out, and change password.
- **Board:** a status list of every feature, with problems first. Each row shows:
  - the feature's state (Down, Slow, Working, Off or Stopped) and one fact;
  - 60 days of uptime bars, with the percentage.

  CPU, RAM, disk and network out are pinned above the list.
- **Feature status page:** each one shows:
  - status and impact, with actions such as Check now;
  - the numbers right now;
  - 60-day uptime;
  - what the feature depends on, such as Nitter instances, Telegram data centres or domains;
  - incidents;
  - the feature's log, filterable to warnings and errors.
- **Server:** the machine, with these parts:
  - CPU and network charts over 15 min, 1 h, 24 h or 7 days, and RAM and disk breakdowns;
  - who can reach whom;
  - the process;
  - the recent server log.
- **Control:**
  - **Service and power:** stop and resume answering, restart, stop and start, start on boot, update and roll back, reboot or shut down the machine, and the import link.
  - **Sources:** Telegram channels, public and private, and X accounts with their Nitter instances. Each list supports add, rename, reorder and remove.
  - **Features:** every Messenger, Telegram-over-DNS and media-relay setting.
  - **Server settings:** fetching, DNS and domains.
  - **This panel:** Account and Appearance.

Every change says when it takes effect, matching how the server reads it. Public channels apply at once; everything else waits for a restart. All waiting changes collect in one notice with Discard and Restart.

The UI works on a phone and on a desktop, in dark and light, in thefeed's own colors and font. It counts readers and never identifies them.

## User Stories

### Signing in

1. As the operator, I want the first visit to a fresh install to ask me to create the admin account, so that nobody else can take over the panel.
2. As the operator, I want first-run setup to require a setup code that the installer printed, so that whoever reaches a fresh public server before me can't claim it.
3. As the operator, I want to choose the admin username, defaulting to "admin", so that I can use a name I'll remember.
4. As the operator, I want the password to be at least 12 characters, not equal to the username, and typed twice, so that a weak password or a typo doesn't lock me out or let others in.
5. As the operator, I want to see the password I'm typing when I choose to, so that I can check it on a phone keyboard.
6. As the operator, I want to be signed in right after creating the account, with a confirmation, so that setup ends in the panel.
7. As the operator, I want a login screen when I'm not signed in, so that the panel is useless to anyone without the password.
8. As the operator, I want a plain "wrong username or password" message that doesn't say which part was wrong, so that a guesser learns nothing.
9. As the operator, I want logins blocked for 5 minutes after 5 wrong passwords, with a countdown of tries left from the third failure, so that password guessing is slow.
10. As the operator, I want my session to last while I use it and to end after 8 hours idle, so that a lost phone doesn't stay signed in forever.
11. As the operator, I want to log out from Control, Account, so that I can end a session on a shared device.
12. As the operator, I want to change my password by entering the current one, so that a stolen open session can't lock me out.
13. As the operator, I want changing the password to log out every other session, so that an old session somewhere else stops working.
14. As the operator, I want to reset a forgotten password from the server's shell with one command that issues a new setup code, so that I'm never locked out of my own server.
15. As the operator, I want the panel served over HTTPS only, so that my password and session never cross the network in clear text.

### Board

16. As the operator, I want every feature as one row with its state word and a coloured dot, so that I can tell what's wrong in a second.
17. As the operator, I want features that need attention grouped first under a count ("2 need attention"), with working and turned-off features below, so that problems are never below the fold.
18. As the operator, I want one fact per row, such as "0 of 3 Nitter instances answer" or "842 queries a minute on 3 domains", so that I know why without opening the page.
19. As the operator, I want 60 days of uptime bars and an uptime percentage per row, so that a bad week shows even when today is fine.
20. As the operator, I want CPU, RAM, disk and network out pinned above the list and updating every few seconds, so that I see the server's load with the feature health.
21. As the operator, I want to pause and resume live updates, so that numbers stop moving while I read them, and so that motion respects my reduced-motion setting.
22. As the operator, I want a count of features needing attention on the Board tab, so that I notice problems from any screen.
23. As the operator, I want every row to say "Stopped" or "Not answering" while the server is stopped or silent, so that the Board never claims things work when they don't.
24. As the operator, I want a feature I turned off and restarted to move to "turned off", so that it isn't counted as a problem.
25. As the operator, I want a clear banner when the panel can't reach the server, showing what it last saw and when, with Try again, so that I don't mistake stale data for live data.
26. As the operator, I want a first-load state that says data is loading, so that a slow link doesn't look broken.

### Feature status pages

27. As the operator, I want each feature's status block to show its state, how long, and what it affects for readers, so that I know how urgent it is.
28. As the operator, I want each feature's actions right in the status block, such as Check now, Edit instances or Tunnel settings, so that I can act where I read.
29. As the operator, I want Check now to run that feature's check at once and show "Checking…" and then the result with a time, so that I can confirm a fix.
30. As the operator, I want the last and next automatic check times, so that I know how fresh the status is.
31. As the operator, I want four "right now" facts per feature, so that I see the numbers that matter for it.
32. As the operator, I want 60-day uptime with a date axis and counts written out ("34 days up, 3 slow, 23 down"), so that I can read history without a legend.
33. As the operator, I want a dependency table per feature, with each one's status and detail, so that I see which part is failing:
    - X: Nitter instances;
    - Telegram over DNS: data centres and dial times;
    - DNS feed: domains and queries a minute;
    - Telegram channels: recent fetch cycles;
    - Messenger: limits;
    - GitHub relay: repository, token and rate limit.
34. As the operator, I want an incidents table (started, lasted, what happened) per feature, with an empty state when there were none, so that I can see patterns.
35. As the operator, I want each feature's own log, newest first and grouped by day, with an "All lines / Warnings and errors" filter, so that I can dig in from my phone.
36. As the operator, I want to edit the Nitter instance list from X's status page, so that I can fix X where I found the problem.
37. As the operator, I want a feature page to say Off or Stopped, with a link to the relevant settings or power page, when it's turned off or the server is stopped, so that I know how to bring it back.

### Server

38. As the operator, I want CPU and network charts with a labelled scale and a time range of 15 min, 1 h, 24 h or 7 days, so that I can see load and spikes.
39. As the operator, I want RAM split into thefeed-server, media cache, system and free, so that I know what uses memory.
40. As the operator, I want disk split into media cache, reports and logs, system and data, and free, so that I know when space will run out.
41. As the operator, I want "who can reach whom", so that I can tell whether a problem is mine or the network's:
    - public resolvers reaching this server;
    - Telegram data centres from here;
    - Nitter instances from here;
    - the GitHub API from here.
42. As the operator, I want process facts: running since, load average, open sockets, and the listen address and port redirect, so that I can sanity-check the service.
43. As the operator, I want the recent server log on this page, so that I can see everything happening at once.
44. As the operator, I want the server name, version, uptime and "update available" at the top, so that I know what I'm running.

### Service and power

45. As the operator, I want to stop the server answering queries while the process stays up, and to resume, so that I can go silent without losing state.
46. As the operator, I want to restart thefeed-server, which applies saved changes, so that waiting changes take effect.
47. As the operator, I want to stop and start thefeed-server while the panel keeps running, so that I can take the service down and bring it back from my phone.
48. As the operator, I want to choose whether the server starts when the machine boots, so that I control what happens after a reboot.
49. As the operator, I want to see the installed version and update to the newest release in one step, so that I don't have to remember the install command.
50. As the operator, I want to roll back to the previous version after an update, so that a bad release is one tap to undo.
51. As the operator, I want an update that fails to start healthy to roll itself back and tell me, so that an update can't leave my readers without a server.
52. As the operator, I want to reboot the machine, so that I can recover from kernel or OS issues remotely.
53. As the operator, I want to shut down the machine only after typing the domain, with a clear warning that only my hosting provider's console turns it back on, so that I can't do it by accident.
54. As the operator, I want every disruptive action to confirm in place and say what readers lose, so that I decide knowingly.
55. As the operator, I want a status line on the power page that says what just happened, such as "Restarted at 12:04", so that I know the action worked.
56. As the operator, I want a banner with Resume or Start on every top-level screen while the server is silent or stopped, so that I don't forget it.
57. As the operator, I want to copy the readers' import link, with a fallback field if copying fails, so that I can share the server with readers.

### Sources: Telegram channels

58. As the operator, I want to add a public channel by @name or t.me link, so that I can add sources from my phone.
59. As the operator, I want public channel changes to apply right away, with a fetch started for the new channel, so that readers get it within the cycle.
60. As the operator, I want channel names validated (5 to 32 letters, digits or underscores, starting with a letter) and duplicates refused, so that I can't break the list.
61. As the operator, I want to search a long channel list, so that I can find one of 40+ quickly.
62. As the operator, I want to rename, move up, move down and remove a channel, with remove confirmed, so that I can manage the list.
63. As the operator, I want each channel to show the number readers see it as, and to be told when a removal shifts the numbers after it, so that I understand the effect on readers.
64. As the operator, I want to paste an invite link (t.me/+…, t.me/joinchat/…, tg://join?invite=…) and have it filed under Private channels, so that I don't have to know the difference.
65. As the operator, I want private channels refused with a clear reason when the Telegram login is logged out, so that I don't add sources that can't work.
66. As the operator, I want private invite links shown truncated, so that a screenshot doesn't leak them.
67. As the operator, I want a "Fetch all now" button with the last and next cycle times, so that I can force a refresh after changes.

### Sources: X accounts

68. As the operator, I want to add an X account by @handle or x.com/twitter.com link, validated (1 to 15 letters, digits or underscores) with duplicates refused, so that the list stays clean.
69. As the operator, I want to rename, reorder and remove X accounts, so that I can manage them.
70. As the operator, I want to add, reorder and remove Nitter instances, always keeping at least one, so that X has somewhere to read from.
71. As the operator, I want X account and instance changes marked "applies after restart", so that I know they aren't live yet.
72. As the operator, I want the X page to say clearly when X is down and link to its status page, so that I can see why accounts are paused.

### Feature and server settings

73. As the operator, I want to turn the Messenger, Telegram over DNS, the DNS media relay and the GitHub media relay on and off, so that I run only what I need.
74. As the operator, I want every Messenger limit editable with its unit and allowed range, so that I can tune abuse limits:
    - sends an hour;
    - inbox size;
    - messages from one sender;
    - message size;
    - expiry;
    - deleting idle addresses;
    - maximum addresses;
    - disk write interval.
75. As the operator, I want every Telegram-over-DNS limit editable, so that I can balance capacity and cost:
    - maximum tunnels;
    - idle close;
    - buffer per tunnel;
    - download speed per tunnel;
    - dial timeout.
76. As the operator, I want the media relay settings editable, so that I can manage media delivery:
    - DNS relay: largest file, cache time and compression;
    - GitHub relay: repository, branch, largest file, keep time and repository budget.
77. As the operator, I want to replace the GitHub token without ever seeing the old one, with the format checked, so that I can rotate it safely.
78. As the operator, I want to set the fetch interval (minimum 3 minutes) and posts per channel, so that I can trade freshness for load.
79. As the operator, I want to see whether the Telegram login is active and to log out of it, so that I can drop the account from the server.
80. As the operator, I want random answer padding, remote management over DNS and query logging editable, with a warning that query logging grows fast, so that I control what the server answers and logs.
81. As the operator, I want my domains listed with what each serves and a "Check NS records" action, so that I can confirm delegation still points here.
82. As the operator, I want out-of-range numbers or a malformed repository name rejected inline with the allowed range, so that a bad value never reaches the server.
83. As the operator, I want each changed setting marked "applies after restart", and a count on the Control list, so that I know what's waiting.
84. As the operator, I want one notice with Discard and Restart whenever changes are waiting, so that I can apply or drop them together.
85. As the operator, I want waiting changes worked out from what the running server reports, not from what the panel remembers, so that hand edits over SSH are shown correctly too.
86. As the operator, I want the panel to refuse a save when the file changed on disk since I opened it, and to ask me to reload, so that the panel never overwrites an edit made elsewhere.

### Account and appearance

87. As the operator, I want to see who is signed in and since when, so that I can spot an unexpected session.
88. As the operator, I want a dark or light theme using thefeed's own colors, with every text color passing WCAG AA, so that I can read it in any light.

### Every screen

89. As the operator, I want each action I take recorded with time and outcome, such as "Restart, settings changed, update", so that I can tell later what changed.
90. As the operator, I want a layout that works from 360 px phones to wide desktops, with one pane on a phone and a list beside details from 640 px, so that I can use it anywhere.
91. As the operator, I want full keyboard use, visible focus, labelled controls and status messages announced to screen readers, so that the panel is accessible.
92. As the operator, I want the panel to work without loading anything from third-party sites, so that it works on a censored network and leaks nothing.
93. As a reader, I want the panel to store only counts and never anything that identifies me, so that running a panel doesn't put me at risk.

### Running in Docker

94. As the operator, I want to run the panel as a Docker container, so that it runs the same way on every server.
95. As the operator installing with the script, I want to be asked whether to run the panel in Docker, with Docker installed for me when it's missing and my answer remembered, so that I choose once.
96. As the operator running thefeed with docker compose, I want the panel to restart, stop and start the server container and set whether it starts with Docker, so that I don't need a shell.
97. As the operator running thefeed with docker compose, I want the server container's log in the panel, split by feature like the journal, so that troubleshooting works the same.
98. As the operator running thefeed with docker compose, I want Update and Rollback to change the server's image with the same health check and automatic rollback, and to stay in effect after the next `docker compose up -d`, so that compose doesn't undo them.
99. As the operator running thefeed with docker compose, I want to be told which compose settings override `thefeed.env`, so that the panel's changes aren't silently ignored.
100. As the operator, I want the panel's edits to leave every server file readable by the server, whatever user it runs as, so that a settings change never locks the server out of its own config.
101. As the operator, I want the Service page to say whether it manages a service or a container, and to offer only the actions that work there, so that nothing on it fails by design.

## Implementation Decisions

### Shape

- **New companion service, `thefeed-admin`.** It's its own binary on the same machine as thefeed-server, run as a Docker container or a systemd unit (see "Running in Docker"). It must keep running while thefeed-server is stopped, restarting or updating, and be able to start it again, so it can't live inside thefeed-server.
- **No build step.** It's one Go binary with the UI (HTML, CSS, vanilla JS) embedded, the same approach as the existing client web UI. It ships thefeed's tokens and icon set and bundles the Vazirmatn font, so nothing loads from third-party sites.
- **Runs as root**, like thefeed-server does today. It has to:
  - manage a root-run systemd unit;
  - write the root-only `thefeed.env`;
  - read the journal;
  - reboot or power off.

  Reducing privileges (a dedicated user plus a narrow polkit or sudoers rule) is listed in Further Notes.

### Network and TLS

- **HTTPS only**, on port 443 by default; the address is configurable.
- **With a panel hostname:** if the operator sets one (an A record pointing at the server), the panel gets and renews a Let's Encrypt certificate itself, using the TLS-ALPN-01 challenge on 443. The ACME client comes from `golang.org/x/crypto`, which is already a dependency. HSTS is on when the certificate is real.
- **Without a hostname:** the panel makes a self-signed certificate on first start, and the installer prints its SHA-256 fingerprint so the operator can check the browser warning.
- **Localhost-only mode:** this mode is for access through an SSH tunnel. It's the only mode that allows plain HTTP.

### The panel's own storage

- **A bbolt database**, already a dependency, in its own directory next to the server data with mode 0600. Holds:
  - the admin account (one record: username, bcrypt hash at cost 12, created time);
  - sessions (SHA-256 of the token, then created, last seen, client hint);
  - the setup code (hash, created);
  - login-failure state;
  - feature samples and daily rollups;
  - incidents;
  - system-metric samples;
  - the action record;
  - panel settings: release source, panel hostname, resolver list for reachability, theme default.
- **Retention:**
  - per-minute feature samples: 7 days;
  - daily feature rollups and incidents: 90 days;
  - per-minute system metrics: 7 days;
  - 10-second system metrics in memory: the last hour;
  - action record: 1 year.

### Authentication

- **Sessions:**
  - A random 256-bit token goes in a cookie (`HttpOnly`, `Secure`, `SameSite=Strict`).
  - Only its SHA-256 is stored.
  - A session ends after 8 hours idle or 7 days at most.
  - Logout deletes the session. Changing the password deletes every other session.
- **CSRF:** `SameSite=Strict`. Every state-changing request must also be JSON with an `Origin` matching the panel's own origin.
- **Throttling:** after 5 consecutive failed logins, every login is refused for 5 minutes, counted globally because there's one admin. Success resets the count. Failed attempts go to the journal with the source address; the database stores only the count.
- **First run:**
  - When no admin exists, the panel makes a setup code: 8 characters from an unambiguous alphabet, shown as `XXXX-XXXX`.
  - Only its hash is stored. The plain code goes to the journal and to a root-only file, and the installer prints it.
  - The code works once and expires after 24 hours; a new one is made at the next start.
  - A CLI subcommand on `thefeed-admin`, `reset-admin`, deletes the admin and all sessions and issues a new code.
  - The mockup's copy says `thefeed-server --reset-admin`. The real command lives on `thefeed-admin`, and the copy changes to match.

### thefeed-server's local control endpoint (new)

HTTP over a Unix socket in the server's data directory, mode 0600. It's on by default, and a flag or env setting turns it off. It's the only way the panel talks to the running server. The contract:

- **`GET` status** returns:
  - `server`: version, started time, `answering` (true/false), and the effective configuration with secrets left out (key, API hash, GitHub token).
  - `feed`: queries per domain, as counters and as per-minute rates.
  - `channels`: the last N fetch cycles (start, duration, channels fetched of total, posts), plus the interval.
  - `x`: per-instance last result (host, ok/error, error text, latency, time) and accounts.
  - `mtproto`: tunnels open and the cap, plus per data centre the last dial time, failures in the last hour and the last error.
  - `chat`: active addresses today, messages today, messages waiting for pickup, inbox-full rejections today, and store errors.
  - `mediaCache`: the existing MediaCacheStats.
  - `ghRelay`: the existing RelayStatus.
- **`POST` channels/reload:** re-read `channels.txt` and apply it, using the same code path as the existing DNS admin add and remove commands.
- **`POST` refresh:** fetch everything now, the same as the existing DNS admin refresh.
- **`POST` checks/{feature}:** run that feature's check and return the result. For X, that's probing every instance. For Telegram over DNS, it's dialling every data centre. For the GitHub relay, it's the relay's own recovery probe. For the Messenger, it's storing and reading back a test message.
- **`POST` answering `{on}`:**
  - **Off:** the DNS listener stays bound but sends no responses, and open Telegram-over-DNS tunnels close.
  - **On:** answering resumes.
  - **Not persisted:** a restart always comes back answering, and the panel says so.
- **Reader changes:** the Telegram, public and X readers each keep a short "last cycle" summary. The X reader keeps per-instance results, and the Telegram-over-DNS relay keeps per-data-centre dial stats. These are small additions that the status endpoint reads.

### Configuration: one source of truth

- **A shared settings registry, used by both binaries.** For each setting it gives:
  - the env name and the matching flag;
  - its type, range and default;
  - which Control page it belongs to;
  - whether it applies live or at restart.

  The server's flag parsing and the panel's validation both use it, so they can't drift.
- **List parsing is shared too.** Parsing channel names, X handles, invite links (the server's existing invite parser) and Nitter instances moves to shared code, so the panel validates exactly as the server parses.
- **`thefeed.env` becomes the single source of every setting.** Flags given on the command line beat env values, so a flag in the systemd unit silently shadows an edit made in the panel. The installer stops passing settings as flags: the unit keeps only `--data-dir` and reads everything else from the env file. On start, the panel detects a unit that still passes shadowing flags and offers to fix it, with a restart.
- **Writes:**
  - `thefeed.env` keeps its comments, order and unknown keys. The write is atomic (temporary file, fsync, rename) and keeps mode 0600.
  - The list files keep their header comment and format:
    - `channels.txt`: `@name` per line;
    - `x_accounts.txt`: a name per line;
    - `private_channels.txt`: one invite link per line.
  - Nitter instances stay in the comma-separated env setting.
  - Writes are serialized. A write is refused with "changed elsewhere, reload" if the file's modification time or hash differs from what the request was based on.
- **What applies live:** public channels apply live, because after writing `channels.txt` the panel calls channels/reload. Every other setting and list applies at restart.
- **Waiting changes:** these are worked out by comparing the files on disk with the effective configuration the running server reports. Discard rewrites the files to match the running configuration.

### Health, uptime and incidents

The panel samples status every 60 seconds (and on demand after a check) and gives each feature one of four states: ok, slow, down or off. The thresholds below are starting values, tunable in the registry:

- **DNS feed:**
  - down: the server isn't answering, or the panel's self-test query (sent every minute to the local listener with the server key) fails;
  - slow: the self-test takes over 1 second;
  - else ok.
- **Telegram channels:**
  - down: no successful cycle in 3 intervals, or the last cycle fetched no channels;
  - slow: the last cycle took over 80% of the interval or fetched under 90% of channels;
  - else ok.
- **X:**
  - off: there are no X accounts;
  - down: no instance answered at the last check;
  - slow: some instances failed;
  - else ok.
- **Telegram over DNS:**
  - off: disabled or no domains;
  - down: every data-centre dial failed in the last 10 minutes;
  - slow: any dial failure in the last hour, or tunnels at 90% or more of the cap;
  - else ok.
- **Messenger:**
  - off: disabled;
  - down: any store error in the last 10 minutes;
  - else ok.
- **DNS media relay:**
  - off: disabled;
  - down: the media cache reports errors;
  - else ok.
- **GitHub relay:**
  - off: disabled;
  - down: the token is denied, the quota is exhausted, or 3 or more failures in a row;
  - slow: more than 50 uploads waiting, or the repository over 90% of its budget;
  - else ok.

The server's state overrides every feature: while the server is stopped or not answering, every feature is down, worded "Stopped" or "Not answering".

**Daily rollup:**
- off: the feature was off all day;
- down: 30 minutes or more down that day;
- slow: any slow or down minutes, but under 30 down;
- else up.

**Uptime %** is days that weren't down divided by days that weren't off, the same rule the mockup uses.

**Incidents:**
- An incident opens when a feature leaves ok for 2 samples in a row, and closes after 2 ok samples in a row.
- It records the start, the end, the worst state, and a summary from the rule that fired.

### System metrics and reachability (Linux only)

- **Read from `/proc` and `statfs`, no new dependencies:**
  - CPU%;
  - RAM total and available;
  - thefeed-server's RSS and open file descriptors;
  - the media cache size, from status;
  - disk use of the data directory and the filesystem;
  - network in and out on the default-route interface;
  - load average.
- **Sampling:** every 10 seconds, kept in memory for an hour, with averages stored per minute.
- **Reachability:**
  - Public resolvers: the panel periodically asks a configurable list of public resolvers for the server's own feed domain and counts how many answer. That proves they can reach this server. The default list has about 10 well-known resolvers.
  - Telegram data centres, Nitter instances and GitHub come from status.

### Logs

- **Source:** the systemd journal for `thefeed-server`, read through `journalctl` JSON output behind an interface. The journal keeps history across restarts.
- **Feature:** each line is matched to a feature by the prefixes the server already logs, such as `[dns]`, `[admin]`, `x:`, `mtproto:`, `chat:`, `github-relay:` and `dns-media:`.
- **Level:** ERROR or WARN comes from keyword rules kept in one table, because the server's log lines carry no level today. Everything else is INFO.
- **Paging:** newest first, limited per request.

### Service, power and update

- **systemd:** behind an interface that runs `systemctl`:
  - restart, stop and start;
  - enable and disable for start on boot;
  - `reboot` and `poweroff`.
- **Logout of Telegram:** sets `THEFEED_NO_TELEGRAM`, deletes the Telegram session file, and restarts. Private channels stop; public channels continue without a login.
- **Update:**
  - The release source is configurable as owner/repo. It defaults to the repo the installer used, so a fork can point at itself.
  - The latest tag is checked with the existing updater's tag lookup and version comparison.
  - The panel downloads the linux/arch asset and verifies its SHA-256 against a `SHA256SUMS` file on the release. The release workflow starts publishing that file. Without a matching checksum the update is refused.
  - It keeps the current binary as the previous version, swaps the new one in with an atomic rename, and restarts.
  - **Health gate:** if the status endpoint doesn't answer healthy within 60 seconds, the panel rolls back automatically and says so.
  - **Rollback:** swaps the previous binary back and restarts.

### Panel JSON API (what the UI calls)

- **Auth:**
  - setup status;
  - create admin, with the code, username and password;
  - login, logout and current session;
  - change password.
- **Board:** one summary call: every feature's state, line, 60-day bars and %, the pinned metrics, the server state and the attention count. The UI polls it every 5 seconds unless paused.
- **Feature:** state, impact, actions, facts, uptime, dependencies, incidents, plus a log page with a filter and a cursor.
- **Checks:** run a feature's check.
- **Server:** metrics for a range, breakdowns, reachability, process, and the recent log.
- **Service:** state, power actions (pause, resume, restart, stop, start, boot on/off, reboot, shutdown with the typed domain), version, update and rollback.
- **Settings:** read everything, each with its live and saved value and a pending flag. Patch one setting. Discard all.
- **Lists:** add, rename, move and remove for public channels, private channels, X accounts and Nitter instances. Each change returns the new list and whether it applied live.
- **Domains:** list and NS check.
- **Account:** the session list, so the operator can spot an unexpected one.
- **Action record:** read.

Every error carries a field and a human message, which the UI shows next to the field.

### Installer

- `install.sh` installs both binaries and both units.
- It asks for an optional panel hostname, and opens 443/tcp if ufw is active.
- At the end it prints the panel URL, the setup code and, when self-signed, the certificate fingerprint.
- It writes every setting to `thefeed.env` and stops baking flags into the unit.
- `--uninstall` removes the panel too.
- It asks "Run the admin panel in Docker? [Y/n]" and saves the answer as `THEFEED_ADMIN_DOCKER` in `thefeed.env`, which is the default on the next run.
  - **Yes** installs Docker from the OS packages when it's missing, then runs the panel container with `docker run` (see "Running in Docker"). `--local <dir>` loads the panel image from an archive in that directory instead of pulling it.
  - **No** keeps the panel as the systemd unit above.
  - Either way it prints the URL, setup code and fingerprint, reading the panel's log from `docker logs` or the journal.

### Running in Docker

- **Two settings say where the panel is.** Neither is detected; the panel's image and the compose file set them.
  - `THEFEED_ADMIN_CONTAINER=1`, set by the panel's image: the panel runs inside a container, so it runs the host's `systemctl` and `journalctl` through `nsenter -t 1 -m -u -i -n -p --`.
  - `THEFEED_ADMIN_SERVER`, `systemd` (the default) or `docker`: how thefeed-server runs. With `docker`, the container is named by `THEFEED_ADMIN_SERVER_CONTAINER`, default `thefeed-server`.
- **The panel's own settings:** thefeed-admin reads `<data-dir>/thefeed.env` itself at start, as thefeed-server does, and the real environment wins. A container then needs no env file, and picks up a changed setting at its next start.
- **The panel container is the same in both cases:**
  - `privileged`, `pid: host` and `network_mode: host`. It's root on the host, as the systemd panel is.
    - `pid: host` lets it find thefeed-server in `/proc`.
    - The host network gives it the host's network counters, port 443 for browsers and Let's Encrypt, and the DNS self-test's way to the server.
  - It mounts `/var/run/docker.sock`, plus `/opt/thefeed` and `/etc/systemd/system` at the same paths, so the binary swap and the unit-file code run unchanged.
  - Its restart policy is `unless-stopped`.
- **Docker Engine API:** `net/http` over the unix socket, with no Docker SDK. Calls used: container inspect, start, stop, restart and update (restart policy), logs, image pull, and container create and remove.
- **thefeed-server as a container (`THEFEED_ADMIN_SERVER=docker`):**
  - **Restart, stop and start** act on the container.
  - **Start on boot** is the restart policy: `unless-stopped` for on, `no` for off.
  - **Reboot and shutdown** act on the host through `nsenter`, as in a script install.
  - **Logs:** the container's log through the API with `timestamps=1`, with the 8-byte stream frames split apart. The paging cursor is a timestamp. Feature matching and levels are as for the journal.
  - **Shadowing:** environment variables and settings flags in the container's config beat `thefeed.env`. The page lists them and says to remove them from `docker-compose.yml`. There's no fix-unit in Docker mode.
  - **Update:**
    - The newest tag is looked up as before, from GitHub releases.
    - The image is the container's current image repository with the new tag. The panel pulls it, then writes `THEFEED_SERVER_TAG=<tag>` into the compose project's `.env` (`/opt/thefeed/.env`).
    - It recreates the container with the same config and the new image. The same 60-second health gate and automatic rollback apply.
    - Writing the tag means a later `docker compose up -d` keeps the panel's version.
  - **Rollback** recreates the container with the previous tag, which the panel remembers, and writes that tag back.
  - **Image integrity:** the registry checks content digests, and `SHA256SUMS` applies only to binaries. Pinning digests is later hardening.
- **Files keep their owner.** The panel writes files in the data directory: `thefeed.env`, the lists and the server key. Each keeps its owner and group as well as its mode. The server container runs as uid 1000, and a root-owned 0600 file would lock it out of its own config.
- **Images:** the release workflow builds `ghcr.io/<owner>/thefeed-server` and `ghcr.io/<owner>/thefeed-admin` (owner lowercased) for amd64 and arm64, and pushes them tagged with the release and `latest`. Pull requests build both images without pushing.
- **Compose file:** `docker-compose.yml` runs both services from those images.
  - **thefeed-server** gets only its data volume (`./data:/data`) and the DNS port. It has no `env_file`, no `environment` and no settings flags, because the real environment beats `thefeed.env`.
  - **Settings** live in `data/thefeed.env`, which has to set `THEFEED_LISTEN=:5300` and `THEFEED_CONTROL_SOCKET=true`.
  - **The panel** mounts the compose directory at `/opt/thefeed`. Its data is then `/opt/thefeed/data`, and the panel's own data is `/opt/thefeed/admin`.
  - **Image tags** come from `.env`: `THEFEED_SERVER_TAG` and `THEFEED_ADMIN_TAG`.
- **reset-admin** in a container prints the Docker commands: stop the panel, run `reset-admin` in a one-off container, start it again.
- **The panel's own update** stays with the installer, or `docker compose pull admin && docker compose up -d`.

## Testing Decisions

**What a good test is here:**
- It drives a public seam and checks behaviour you can see: HTTP status and JSON, files on disk, commands sent to systemd, DNS answers.
- It never checks internal structs or call order inside a module.
- One flow per test, written as the operator would do it.

### Seam 1: the panel's JSON API

This is where most tests go. Tests run the real handler with `httptest`. Everything outside the process sits behind small interfaces with fakes:

- thefeed-server's control client;
- systemd, as a fake that records commands;
- the journal;
- `/proc`;
- the clock;
- the release downloader.

Files and the database live in a temporary directory.

Flows to cover:

1. **First run:**
   - a wrong code is rejected;
   - short and mismatched passwords are rejected;
   - success signs you in;
   - the code can't be used twice.
2. **Login lockout:**
   - the 5th failure locks for 5 minutes, using the fake clock;
   - after that, login works.
3. **Sessions:** a session expires when idle; changing the password ends other sessions.
4. **CSRF:** a mutating request with a foreign `Origin` is refused.
5. **Public channels:**
   - adding a channel writes `channels.txt` and calls reload;
   - invalid names and duplicates are refused;
   - an invite link lands in `private_channels.txt` as waiting.
6. **Lists:** rename, reorder and remove work for every list, and the last Nitter instance can't be removed.
7. **Settings:**
   - an out-of-range value is refused, and the file is untouched;
   - a valid patch writes the env file, keeping comments and unknown keys;
   - the change shows as waiting until the faked status reports it as the running value.
8. **Changed on disk:** an edit made on disk after reading is refused.
9. **Discard:** restores the files to the running configuration.
10. **Power:** each action sends the right `systemctl` command, and shutdown is refused without the typed domain.
11. **Update:**
    - a bad checksum is refused;
    - a successful update swaps binaries and restarts;
    - an unhealthy start rolls back;
    - manual rollback works.
12. **Health:**
    - faked status sequences produce the right states, daily rollups, uptime % and incidents;
    - a stopped server marks every feature down.
13. **Logs:** lines are matched to features, levels are classified, and the warnings filter works.
14. **Shadowing flags:** a unit whose `ExecStart` passes shadowing flags is detected.

Prior art: the `internal/web` handler tests, which build a `Server` on `t.TempDir()` and use `httptest` recorders.

### Seam 2: thefeed-server's control endpoint

Tests build a real server in-process on a temporary data directory, and fake upstreams with `httptest` servers for Nitter and GitHub. They cover:

- the status shape;
- channels/reload applying a changed `channels.txt` live;
- refresh starting a cycle;
- every check with healthy and failing upstreams;
- answering off, under which a real DNS query gets no response and tunnels close, then answering back on.

Prior art: `dns_multidomain_test`, `media_integration_test`, and `github_relay_test` with its `httptest` upstreams.

### Seam 6: a fake Docker Engine API

Tests drive the panel's JSON API (seam 1) with the real Docker adapter. The adapter talks to an `httptest` server on a unix socket that plays the Engine API:
- container state;
- start, stop and restart;
- the restart policy;
- logs as multiplexed frames;
- image pull;
- container create and remove.

Tests check what the operator sees through the API and the calls the fake received. For example:
- an update pulls the new tag, writes it to `.env`, and recreates the container with the same config;
- a failed health gate recreates the container with the old tag.

What it catches:
- wrong requests;
- frame parsing;
- the order of update and rollback.

What it misses: real Docker's quirks. The Docker setup of the end-to-end suite covers those.

### Shared registry and parsers

Table tests prove that:
- the server's flag parsing and the panel's validation accept and reject the same values;
- every flag has an env entry.

Prior art: `install_defaults_test`, which checks install defaults against the server's.

### UI

Zero-dependency node unit tests cover the UI's pure functions: uptime bars and %, number and time formatting, and log grouping. They run from `go test` and are skipped when node isn't installed. Prior art: `internal/web/jstest`.

### End-to-end on real Linux (opt-in)

This suite runs on a disposable host with real systemd and journal. In CI that's a GitHub Actions Ubuntu runner, which has systemd and sudo. Locally it's a systemd-enabled container or VM. It sits behind a build tag or Make target, not plain `go test`.

The suite installs locally built binaries with `install.sh`, then drives the panel's HTTPS API (self-signed):

1. **First run:** set up the admin with the code read from the journal, then log in.
2. **Restart:** change a restart-only setting, see it waiting, restart, then confirm the server's PID changed and the setting is live in status.
3. **Public channel:** add one, and see it live without a restart.
4. **Answering:** stop answering and see a real DNS query go unanswered, then resume.
5. **Stop and start:** both work, and the Board shows the change.
6. **Update:** update to a release served from a local fake source, with checksums, then roll back.
7. **Pause and lock:** stop answering survives a panel restart, and login lockout works end to end.

Reboot and shutdown aren't exercised. Seam 1 covers the commands they send.

The same flows run in three setups, each its own CI job:
1. a script install with the panel in Docker, which is the installer's default answer;
2. a script install with the panel as a systemd service;
3. docker compose, with the server and the panel as containers. Update and rollback pull from a local registry (`registry:2`) that holds the server image at two tags.

**Not tested automatically:** visual layout (covered by the mockup and manual review) and Let's Encrypt issuance (the e2e suite uses self-signed).

## Out of Scope

- More than one admin, roles, or two-factor sign-in.
- Signing in to Telegram from the panel. The panel can log out of Telegram but points to `--login-only` for logging in.
- Rotating the server key or passphrase, changing the listen address, and adding or removing domains. Domains are read-only, with an NS check.
- Updating `thefeed-admin` itself from the panel. The installer updates it.
- Alerts and notifications (email, Telegram, push) when a feature goes down.
- Exporting telemetry to external systems such as OpenObserve or Prometheus.
- Managing more than one server.
- Hosting on anything but Linux. thefeed-server runs under systemd or as a Docker container, and the panel runs as a Docker container or under systemd. Kubernetes, Podman and Docker Swarm are out.
- Running thefeed-server in Docker without compose. The panel recreates a container it didn't create only through compose's `.env` tag.
- Reaching the panel over the DNS tunnel. On a network where HTTPS to the server is blocked, use localhost-only mode through an SSH tunnel.

## Further Notes

- **Design source:** the approved mockup is `.scratch/admin-panel/mockup.html`, a copy of `panel-v3.html`. It has the layout, copy, states and flows. The antislop audit for it recorded these accessibility and contrast decisions:
  - the light theme's status colors `#276a2b` and `#8a4f00`;
  - the off-day bar token;
  - 44 px targets;
  - named container queries at 640 and 980 px.

  `PRODUCT.md` holds the product principles, including "count, never identify" and one operator with one server.
- **Calls the agent made, confirmed by the operator on 2026-11-03:**
  - a separate companion service running as root;
  - HTTPS on 443, with Let's Encrypt or self-signed;
  - bcrypt and the session and lockout numbers;
  - bbolt for storage;
  - a Unix-socket control endpoint;
  - journald as the log source;
  - keyword-based log levels;
  - every health threshold;
  - the retention periods;
  - the update checksum requirement and auto-rollback;
  - making `thefeed.env` the single source and removing flags from the unit.
- **Confirmed by the operator on 2026-11-04:**
  - The panel runs as a Docker container and controls thefeed-server, as a systemd service or as a container.
  - The installer asks whether to run the panel in Docker, with Yes as the default. No keeps the systemd panel.
  - In Docker mode, the panel writes the server's image tag to compose's `.env`, so `docker compose up -d` keeps an update or rollback.
  - Test seam 6 (a fake Docker Engine API) and the Docker setups of the end-to-end suite.
- **Fork:**
  - This repo is a fork, and the installer currently downloads upstream releases (`sartoopjj/thefeed`).
  - The panel's configurable release source and the release workflow's new `SHA256SUMS` have to be in place before panel updates can work for the fork.
- **Later hardening:**
  - Run `thefeed-admin` as a dedicated user with a narrow polkit rule for the few `systemctl` actions it needs, and group write access to the data directory.
  - Consider a second factor once there's more than one way in.
