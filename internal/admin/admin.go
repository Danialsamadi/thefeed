// Package admin is the core of thefeed-admin, the web panel that runs beside
// thefeed-server on the same machine: its storage, HTTPS, sign-in and the
// action record. Feature areas plug in as modules.
//
// # Modules
//
// Each feature area (health, sysmetrics, logs, settings, service, the web UI)
// lives in its own package under internal/admin and exports a Module:
//
//	func Module(d *admin.Deps) error
//
// cmd/thefeed-admin lists the modules, one line each, and New calls them in
// that order once the core routes are in place. Inside Module, a module:
//   - adds signed-in API routes with d.Handle("GET /api/<module>/...", h);
//     the core answers 401 unless the request carries a live session;
//   - adds pre-login routes (the UI shell and its assets) with d.HandlePublic;
//   - gets its own bbolt buckets with d.Bucket("<module>.<name>"), created on
//     first use; bucket names start with the module's name;
//   - appends what the operator did to the action record with d.Record;
//   - reads the time only from d.Clock, and runs background loops on d.Ctx,
//     which is cancelled when the app closes.
//
// Every request that isn't GET, HEAD or OPTIONS has already been checked for
// a JSON body and an Origin matching the panel's own before it reaches any
// handler. Answer with WriteJSON and WriteError so every API has one shape:
// errors are {"errors":[{"field":"...","message":"..."}]}, field "" for the
// whole form.
//
// # Core API
//
//	GET  /api/auth/setup        {"needed":bool,"reset":"..."}: no admin yet, setup code
//	                            required; reset is the shell one-liner that resets it
//	POST /api/auth/setup        {code,username,password,repeat} → session
//	POST /api/auth/login        {username,password} → session; 401 with triesLeft,
//	                            429 with retryAfter (seconds) while locked
//	POST /api/auth/logout       ends this session
//	GET  /api/auth/session      {username,since}, or 401
//	POST /api/auth/password     {current,password,repeat}; ends every other session
//	GET  /api/account/sessions  {"sessions":[{since,lastSeen,client,current}]}
//	GET  /api/actions?limit=N   {"actions":[{at,action,outcome}]}, newest first
//
// # Files in the admin directory
//
//	admin.db        the bbolt database, 0600
//	setup-code      the plain setup code while no admin exists, 0600
//	tls/cert.pem    the self-signed certificate (and tls/key.pem)
//	acme/           the Let's Encrypt cache, with a panel hostname
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/sartoopjj/thefeed/internal/controlapi"
)

// Clock is where the panel reads the time, so tests can move it.
type Clock interface{ Now() time.Time }

// ClockFunc turns a func into a Clock: ClockFunc(time.Now) is the real one.
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

// Config is how thefeed-admin is started.
type Config struct {
	AdminDir      string // the panel's own files: database, setup code, certificates
	ServerDataDir string // thefeed-server's data directory
	Listen        string // default ":443"
	Hostname      string // panel hostname for Let's Encrypt; empty means self-signed
	LocalhostOnly bool   // bind 127.0.0.1 with plain HTTP, for an SSH tunnel
	Clock         Clock  // nil means the real clock
}

// Module wires one feature area into the app. See the package doc.
type Module func(*Deps) error

// Deps is what the app hands each module.
type Deps struct {
	Clock         Clock
	ServerDataDir string
	Ctx           context.Context // cancelled by App.Close
	app           *App
}

// Handle adds a route that only a signed-in operator reaches.
func (d *Deps) Handle(pattern string, h http.HandlerFunc) {
	d.app.mux.Handle(pattern, d.app.signedIn(h))
}

// HandlePublic adds a route reachable before sign-in, such as the UI shell.
func (d *Deps) HandlePublic(pattern string, h http.Handler) { d.app.mux.Handle(pattern, h) }

// Bucket returns the module's bucket called name, creating it if needed.
func (d *Deps) Bucket(name string) (*Bucket, error) { return newBucket(d.app.db, name) }

// Record appends a to the action record. A zero At means now.
func (d *Deps) Record(a Action) error { return d.app.record(a) }

// Control is thefeed-server's control socket.
func (d *Deps) Control() controlapi.Client {
	return controlapi.New(filepath.Join(d.ServerDataDir, controlapi.SocketName))
}

// EnvFile is thefeed-server's settings file in its data directory dataDir.
func EnvFile(dataDir string) string { return filepath.Join(dataDir, "thefeed.env") }

// App is a running panel: its database, routes and modules.
type App struct {
	cfg    Config
	clock  Clock
	db     *bolt.DB
	mux    *http.ServeMux
	cancel context.CancelFunc

	authMu                  sync.Mutex // one sign-in flow at a time: keeps the failure count and the setup code exact
	auth, sessions, actions *Bucket
}

// New opens the panel's database, makes a setup code if no admin exists yet,
// and wires the core routes and then each module.
func New(cfg Config, modules ...Module) (*App, error) {
	if cfg.Clock == nil {
		cfg.Clock = ClockFunc(time.Now)
	}
	db, err := openDB(cfg.AdminDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{cfg: cfg, clock: cfg.Clock, db: db, mux: http.NewServeMux(), cancel: cancel}
	if err := a.init(); err != nil {
		a.Close()
		return nil, err
	}
	d := &Deps{Clock: cfg.Clock, ServerDataDir: cfg.ServerDataDir, Ctx: ctx, app: a}
	for _, m := range modules {
		if err := m(d); err != nil {
			a.Close()
			return nil, err
		}
	}
	return a, nil
}

func (a *App) init() (err error) {
	if a.auth, err = newBucket(a.db, "auth"); err != nil {
		return err
	}
	if a.sessions, err = newBucket(a.db, "auth.sessions"); err != nil {
		return err
	}
	if a.actions, err = newBucket(a.db, "actions"); err != nil {
		return err
	}
	if err := a.ensureSetupCode(); err != nil {
		return err
	}
	a.mux.HandleFunc("GET /api/auth/setup", a.handleSetupStatus)
	a.mux.HandleFunc("POST /api/auth/setup", a.handleSetup)
	a.mux.HandleFunc("POST /api/auth/login", a.handleLogin)
	a.mux.HandleFunc("POST /api/auth/logout", a.handleLogout)
	a.mux.Handle("GET /api/auth/session", a.signedIn(a.handleSession))
	a.mux.Handle("POST /api/auth/password", a.signedIn(a.handlePassword))
	a.mux.Handle("GET /api/account/sessions", a.signedIn(a.handleSessions))
	a.mux.Handle("POST /api/account/sessions/end", a.signedIn(a.handleEndSession))
	a.mux.Handle("GET /api/actions", a.signedIn(a.handleActions))
	return nil
}

// Close stops the modules' background work and closes the database.
func (a *App) Close() error {
	a.cancel()
	return a.db.Close()
}

// Handler is the whole panel: security headers, the localhost Host check and
// the CSRF check in front of every route.
func (a *App) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if a.cfg.Hostname != "" {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		// Plain HTTP on loopback is open to DNS rebinding: a page on any name
		// that resolves to 127.0.0.1 would be same-origin with itself here.
		if a.cfg.LocalhostOnly && !loopbackHost(r.Host) {
			WriteError(w, http.StatusForbidden, FieldError{Message: "Open the panel as localhost or 127.0.0.1."})
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !sameOrigin(r) || !isJSON(r) {
				WriteError(w, http.StatusForbidden, FieldError{Message: "Refused: not a same-origin JSON request."})
				return
			}
		}
		a.mux.ServeHTTP(w, r)
	})
}

// FieldError is one problem with a request: Field names the input it's
// about ("" for the whole form), Message says it in words for the operator.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type errorBody struct {
	Errors     []FieldError `json:"errors"`
	TriesLeft  int          `json:"triesLeft,omitempty"`
	RetryAfter int          `json:"retryAfter,omitempty"`
}

// WriteJSON answers with v as JSON.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError answers with the panel's error shape.
func WriteError(w http.ResponseWriter, status int, errs ...FieldError) {
	WriteJSON(w, status, errorBody{Errors: errs})
}

// ReadJSON decodes the request body into v, answering 400 when it can't.
func ReadJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, FieldError{Message: "The request isn't valid JSON."})
		return false
	}
	return true
}
