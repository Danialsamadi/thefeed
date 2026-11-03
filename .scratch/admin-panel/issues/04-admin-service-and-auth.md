# 04 thefeed-admin service skeleton, storage, HTTPS and sign-in API

Status: ready-for-agent
Blocked by: none
Spec: `.scratch/admin-panel/spec.md`, sections "Shape", "Network and TLS", "The panel's own storage", "Authentication" and "Panel JSON API" (Auth, Account, Action record)

## What

- **Binary.** A new `thefeed-admin` (suggested at `cmd/thefeed-admin`) with:
  - `serve` (the default): flags and env for listen address (default `:443`), panel hostname, localhost-only mode, admin data directory, and the server's data directory;
  - `reset-admin`.
- **HTTPS.**
  - With a panel hostname: Let's Encrypt via `golang.org/x/crypto/acme/autocert` (TLS-ALPN-01), with HSTS on.
  - Otherwise: a self-signed certificate made on first run and stored, with its SHA-256 fingerprint logged.
  - Localhost-only mode binds `127.0.0.1`, and only it may use plain HTTP.
- **Storage.** A bbolt store, mode 0600, in its own directory. Give each module a bucket helper, so later tickets add buckets without editing a shared file.
- **Sign-in:**
  - **First run:** when no admin exists, make a setup code (8 characters from an unambiguous alphabet, shown `XXXX-XXXX`). Store only its hash, log the plain code, and write it to a root-only file. It's single-use and expires after 24 hours, with a new one at the next start.
  - **API:**
    - setup status;
    - create admin, from code, username and password, with the spec's rules;
    - login, logout and current session;
    - change password, which needs the current one and ends other sessions;
    - session list.
  - **Passwords:** bcrypt at cost 12.
  - **Sessions:** a 256-bit token in a cookie (`HttpOnly`, `Secure`, `SameSite=Strict`). Store only its SHA-256. A session ends after 8 hours idle or 7 days at most.
  - **CSRF:** every state-changing request must be JSON with an `Origin` that matches.
  - **Throttling:** after 5 consecutive failures, refuse logins for 5 minutes (global). Failures are logged with the source address; only the count is stored.
  - **`reset-admin`:** deletes the admin and sessions and issues a new setup code.
- **Action record.** A store and read API for timestamped actions with an outcome. Later tickets append to it.
- **Module wiring.** Each feature module (health, metrics, logs, settings, service) registers its own routes and buckets in its own file or package. Adding a module should be one line in the app wiring.
- **The clock** is behind an interface, so tests can fake time.

## Acceptance

`httptest` tests on the API, with a temporary directory and a fake clock:

- a wrong code is rejected;
- short or mismatched passwords are rejected;
- success sets the session cookie;
- the code can't be reused;
- the 5th failure locks login, and after 5 minutes on the fake clock login works;
- a session expires when idle;
- changing the password ends other sessions;
- a POST with a foreign `Origin` is refused;
- `reset-admin` produces a working new code.

Prior art: the handler tests in `internal/web`.

## Notes

- This ticket has no UI; ticket 05 adds it.
- Don't touch `internal/server`.
