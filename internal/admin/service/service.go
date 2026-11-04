// Package service is the panel's Service and power page: thefeed-server's
// state, stopping and resuming answers, restart, stop and start, start on
// boot, reboot and shutdown, update and rollback, and moving settings that
// the systemd unit passes as flags into thefeed.env.
//
// # API
//
//	GET  /api/service                State
//	GET  /api/service/import-link    {"link"}: the readers' thefeed:// link, as --print-config prints it
//	POST /api/service/{action}       does it, then answers State
//
// The actions, with their JSON bodies:
//
//	pause, resume                    stop or resume answering DNS (the control socket)
//	restart, stop, start             systemctl restart|stop|start thefeed-server
//	boot-on, boot-off                systemctl enable|disable thefeed-server
//	reboot                           systemctl reboot
//	shutdown {domain}                systemctl poweroff, once the typed domain matches THEFEED_DOMAIN
//	update {version}                 install that release, restart, and roll back if it isn't healthy in 60 s
//	rollback                         put the version the last update replaced back, and restart
//	fix-unit                         move the unit's shadowing flags into thefeed.env, and restart
//	source {repo}                    the owner/repo updates come from; "" for the default
//
// One action runs at a time: another answers 409. An action that was turned
// down before doing anything answers 400 or 409 with the field it's about;
// one that failed answers 502, and is in the action record like every other.
//
// # Updates
//
// Releases come from GitHub, or from THEFEED_ADMIN_RELEASE_BASE_URL in
// thefeed.env when set: the newest tag, looked up at most once an hour,
// and the assets thefeed-server-linux-<arch> and SHA256SUMS. A
// download without a matching SHA256SUMS line installs nothing. The
// installed binary is kept as <binary>.previous. No version older than
// v0.40.1-rc0 is installed, by update or by rollback.
package service

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/controlapi"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
	"github.com/sartoopjj/thefeed/internal/update"
)

// unit is thefeed-server's systemd unit.
const unit = "thefeed-server"

// Systemd runs systemctl with args and returns what it printed.
type Systemd interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

type realSystemd struct{}

func (realSystemd) Run(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return out, fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), bytes.TrimSpace(ee.Stderr))
	}
	return out, err
}

// Releases is where updates come from: repo is owner/name on GitHub.
type Releases interface {
	Latest(ctx context.Context, repo string) (tag string, err error)
	Download(ctx context.Context, repo, tag, name string) (io.ReadCloser, error)
}

// realReleases downloads from base(): github.com unless
// THEFEED_ADMIN_RELEASE_BASE_URL in thefeed.env names a server laid out
// like it, such as the end-to-end suite's fake release.
type realReleases struct{ base func() string }

func (g realReleases) Latest(ctx context.Context, repo string) (string, error) {
	return update.LatestTag(ctx, g.base(), repo)
}

func (g realReleases) Download(ctx context.Context, repo, tag, name string) (io.ReadCloser, error) {
	return update.OpenAsset(ctx, g.base(), repo, tag, name)
}

// Options are the module's edges. Tests fake them; Module uses the real ones.
type Options struct {
	Systemd  Systemd
	Control  controlapi.Client                    // only Status and SetAnswering are used
	Releases Releases                             // nil: GitHub, or THEFEED_ADMIN_RELEASE_BASE_URL
	Sleep    func(context.Context, time.Duration) // nil: time passes
	Binary   string                               // thefeed-server; default: next to the data directory, as install.sh puts it
	Unit     string                               // its unit file; default /etc/systemd/system/thefeed-server.service
}

// Module is the service module on the real machine.
func Module(d *admin.Deps) error {
	return New(Options{Systemd: realSystemd{}, Control: controlapi.New(filepath.Join(d.ServerDataDir, controlapi.SocketName))})(d)
}

// Line is the status line: what the panel last did, and when.
type Line struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// saved is what the panel remembers between requests and restarts.
type saved struct {
	Last     *Line     `json:"last,omitempty"`
	Set      string    `json:"set,omitempty"` // paused or stopped: what the panel last put the server in
	SetAt    time.Time `json:"setAt"`
	Version  string    `json:"version,omitempty"`  // the version last seen running
	Previous string    `json:"previous,omitempty"` // the version kept as <binary>.previous
	Repo     string    `json:"repo,omitempty"`     // the release source; "" means the default
}

// State is GET /api/service.
type State struct {
	State   string    `json:"state"` // running, paused or stopped
	Since   time.Time `json:"since,omitzero"`
	Started time.Time `json:"started,omitzero"`
	Boot    bool      `json:"boot"`    // systemd starts it when the machine boots
	Version string    `json:"version"` // installed: as last seen running
	// Previous is the version a rollback goes back to; "" when there's none.
	Previous string `json:"previous"`
	Latest   string `json:"latest"` // newest release; "" when GitHub didn't say
	Update   bool   `json:"update"` // latest is newer than version
	Source   string `json:"source"` // owner/repo releases come from
	Domain   string `json:"domain"` // what to type to confirm a shutdown
	Listen   string `json:"listen"` // the address it answers DNS on, while it runs
	Last     *Line  `json:"last,omitempty"`
	// Shadowing lists the flags thefeed-server's unit passes that beat
	// thefeed.env, such as "--domain"; fix-unit moves them into the file.
	Shadowing []string `json:"shadowing"`
}

type mod struct {
	Options
	d     *admin.Deps
	store *admin.Bucket
	mu    sync.Mutex // one action at a time
	smu   sync.Mutex // one change to the saved state at a time

	lmu    sync.Mutex // one GitHub lookup at a time
	latest latest
}

type latest struct {
	repo, tag string
	at        time.Time
}

// New is the service module on o.
func New(o Options) admin.Module {
	return func(d *admin.Deps) error {
		store, err := d.Bucket("service.state")
		if err != nil {
			return err
		}
		o.Binary = cmp.Or(o.Binary, filepath.Join(filepath.Dir(filepath.Clean(d.ServerDataDir)), "thefeed-server"))
		o.Unit = cmp.Or(o.Unit, "/etc/systemd/system/thefeed-server.service")
		if o.Sleep == nil {
			o.Sleep = func(ctx context.Context, d time.Duration) {
				select {
				case <-ctx.Done():
				case <-time.After(d):
				}
			}
		}
		m := &mod{Options: o, d: d, store: store}
		if m.Releases == nil {
			m.Releases = realReleases{func() string { return cmp.Or(m.env("THEFEED_ADMIN_RELEASE_BASE_URL"), "https://github.com") }}
		}
		d.Handle("GET /api/service", func(w http.ResponseWriter, r *http.Request) { m.reply(w, r.Context()) })
		d.Handle("POST /api/service/{action}", m.act)
		d.Handle("GET /api/service/import-link", m.importLink)
		return nil
	}
}

func (m *mod) load() (s saved) {
	m.store.Get("state", &s)
	return s
}

// edit changes the saved state. The page's polls and an action in flight
// both write it, so each change is read, made and written under smu.
func (m *mod) edit(fn func(*saved)) error {
	m.smu.Lock()
	defer m.smu.Unlock()
	sv := m.load()
	fn(&sv)
	return m.store.Put("state", sv)
}

func (m *mod) state(ctx context.Context) (State, error) {
	sv := m.load()
	out := State{Last: sv.Last, Domain: m.domain(), Shadowing: m.shadowed()}
	props, err := m.show(ctx)
	if err != nil {
		return out, err
	}
	out.State, out.Boot, out.Version = "stopped", props["UnitFileState"] == "enabled", sv.Version
	if props["ActiveState"] == "active" {
		out.State = "running"
		if st, err := m.status(ctx); err == nil {
			out.Since, out.Started = st.Server.Started, st.Server.Started
			if !st.Server.Answering {
				out.State = "paused"
			}
			out.Version, out.Listen = st.Server.Version, st.Server.Config["server.listen"]
		}
	}
	// Since when it's paused or stopped is when the panel did it; otherwise
	// it's unknown. Running again clears that.
	switch {
	case out.State == sv.Set:
		out.Since = sv.SetAt
	case out.State == "paused":
		out.Since = time.Time{}
	}
	if running := out.State == "running"; out.Version != sv.Version || running && sv.Set != "" {
		m.edit(func(s *saved) {
			s.Version = out.Version
			if running {
				s.Set = ""
			}
		})
	}
	out.Previous, out.Source = sv.Previous, m.source(sv)
	out.Latest = m.newest(ctx, out.Source)
	out.Update = update.IsNewer(out.Latest, out.Version)
	return out, nil
}

// status asks the running server, giving up after a few seconds: the
// panel's page polls, and a hung server mustn't hang it.
func (m *mod) status(ctx context.Context) (*controlapi.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return m.Control.Status(ctx)
}

// source is the GitHub repo updates come from: the panel's setting, else
// the repo the installer used, else upstream.
func (m *mod) source(sv saved) string {
	return cmp.Or(sv.Repo, m.env("THEFEED_ADMIN_RELEASE_REPO"), update.Repo)
}

// newest is the newest release tag of repo, asked of GitHub at most once an
// hour; "" when GitHub didn't say.
//
// The poll that finds it stale waits for GitHub, up to 10 s an
// hour; a background refresh if that's ever noticed.
func (m *mod) newest(ctx context.Context, repo string) string {
	m.lmu.Lock()
	defer m.lmu.Unlock()
	now := m.d.Clock.Now()
	if c := m.latest; c.repo != repo || now.Sub(c.at) >= time.Hour {
		// Not the poll's own context: a dropped poll mustn't blank the answer for an hour.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		tag, _ := m.Releases.Latest(ctx, repo)
		m.latest = latest{repo: repo, tag: tag, at: now}
	}
	return m.latest.tag
}

// show reads the unit's state from systemd.
func (m *mod) show(ctx context.Context) (map[string]string, error) {
	out, err := m.Systemd.Run(ctx, "show", unit, "--property=ActiveState,UnitFileState")
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			props[k] = v
		}
	}
	return props, nil
}

func (m *mod) reply(w http.ResponseWriter, ctx context.Context) {
	s, err := m.state(ctx)
	if err != nil {
		admin.WriteError(w, http.StatusBadGateway, admin.FieldError{Message: "Couldn't ask systemd about thefeed-server: " + err.Error()})
		return
	}
	admin.WriteJSON(w, http.StatusOK, s)
}

func (m *mod) act(w http.ResponseWriter, r *http.Request) {
	ctx := context.WithoutCancel(r.Context())
	if !m.mu.TryLock() {
		admin.WriteError(w, http.StatusConflict, admin.FieldError{Message: "Another action is still running. Wait for it to finish."})
		return
	}
	defer m.mu.Unlock()
	var body struct {
		Domain  string `json:"domain"`
		Version string `json:"version"`
		Repo    string `json:"repo"`
	}
	if !admin.ReadJSON(w, r, &body) {
		return
	}
	var err error
	switch name := r.PathValue("action"); name {
	case "update":
		err = m.update(ctx, body.Version)
	case "rollback":
		err = m.rollback(ctx)
	case "fix-unit":
		err = m.fixUnit(ctx)
	case "source":
		err = m.setSource(strings.TrimSpace(body.Repo))
	default:
		a, ok := power[name]
		if !ok {
			admin.WriteError(w, http.StatusNotFound, admin.FieldError{Message: "There's no such action."})
			return
		}
		err = m.power(ctx, a, body.Domain)
	}
	var no *refused
	switch {
	case errors.As(err, &no):
		admin.WriteError(w, no.status, no.FieldError)
	case err != nil:
		admin.WriteError(w, http.StatusBadGateway, admin.FieldError{Message: err.Error()})
	default:
		m.reply(w, ctx)
	}
}

// refused is an action turned down before anything happened, so it isn't in
// the action record.
type refused struct {
	status int
	admin.FieldError
}

func (r *refused) Error() string { return r.Message }

// note puts an action and how it went in the action record, and passes err on.
func (m *mod) note(name string, err error) error {
	outcome := "Done"
	if err != nil {
		outcome = err.Error()
	}
	m.d.Record(admin.Action{Action: name, Outcome: outcome})
	return err
}

// line sets the status line ("" clears it) and the state the panel put the server in.
func (m *mod) line(text, set string) {
	now := m.d.Clock.Now()
	m.edit(func(sv *saved) {
		sv.Last, sv.Set, sv.SetAt = nil, set, now
		if text != "" {
			sv.Last = &Line{At: now, Text: text}
		}
	})
}

func (m *mod) power(ctx context.Context, a action, typed string) error {
	if a.typed {
		d := m.domain()
		if d == "" {
			return &refused{http.StatusConflict, admin.FieldError{Field: "domain", Message: "THEFEED_DOMAIN isn't set in thefeed.env, so there's nothing to type to confirm."}}
		}
		if !strings.EqualFold(strings.TrimSpace(typed), d) {
			return &refused{http.StatusBadRequest, admin.FieldError{Field: "domain", Message: "Type " + d + " to shut the machine down."}}
		}
	}
	done := func() {
		if !a.keep {
			m.line(a.last, a.set)
		}
		m.note(a.name, nil)
	}
	// Rebooting or shutting down may take the panel with it before
	// systemctl returns, so those are written down first.
	if a.down {
		done()
	}
	if err := a.run(m, ctx); err != nil {
		return m.note(a.name, err)
	}
	if !a.down {
		done()
	}
	return nil
}

// action is one power action. It leaves last as the status line, and the
// server set: paused, stopped, or "" for running. keep leaves both as they were.
type action struct {
	name, last, set string
	keep            bool
	down            bool // takes the machine down
	typed           bool // the operator types the domain to confirm
	run             func(*mod, context.Context) error
}

func systemctl(args ...string) func(*mod, context.Context) error {
	return func(m *mod, ctx context.Context) error { _, err := m.Systemd.Run(ctx, args...); return err }
}

func answering(on bool) func(*mod, context.Context) error {
	return func(m *mod, ctx context.Context) error { return m.Control.SetAnswering(ctx, on) }
}

var power = map[string]action{
	"pause":    {name: "Stop answering", set: "paused", run: answering(false)},
	"resume":   {name: "Resume answering", last: "Answering again", run: answering(true)},
	"restart":  {name: "Restart", last: "Restarted", run: systemctl("restart", unit)},
	"stop":     {name: "Stop server", set: "stopped", run: systemctl("stop", unit)},
	"start":    {name: "Start server", last: "Started", run: systemctl("start", unit)},
	"boot-on":  {name: "Start on boot on", keep: true, run: systemctl("enable", unit)},
	"boot-off": {name: "Start on boot off", keep: true, run: systemctl("disable", unit)},
	// With start on boot off, the server stays stopped from here.
	"reboot":   {name: "Reboot", last: "Machine rebooted", set: "stopped", down: true, run: systemctl("reboot")},
	"shutdown": {name: "Shut down", keep: true, down: true, typed: true, run: systemctl("poweroff")},
}

// env reads key from thefeed.env; "" when it isn't there.
func (m *mod) env(key string) string {
	data, _, _ := serverconfig.ReadFile(filepath.Join(m.d.ServerDataDir, "thefeed.env"))
	v, _ := serverconfig.ParseEnv(data).Get(key)
	return v
}

func (m *mod) domain() string { return m.env("THEFEED_DOMAIN") }

// importLink answers {"link"}: the readers' thefeed:// link, built from
// thefeed.env and the server's signing key as thefeed-server --print-config
// builds it. It holds the key: it's not logged or recorded.
func (m *mod) importLink(w http.ResponseWriter, r *http.Request) {
	domain, pass := m.domain(), m.env("THEFEED_KEY")
	if domain == "" || pass == "" {
		admin.WriteError(w, http.StatusConflict, admin.FieldError{Message: "THEFEED_DOMAIN and THEFEED_KEY must both be set in thefeed.env to make the link."})
		return
	}
	key, err := serverconfig.LoadOrCreateServerKey(m.d.ServerDataDir)
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't read the server's signing key: " + err.Error()})
		return
	}
	link := serverconfig.ConfigURI(domain, strings.Split(m.env("THEFEED_EXTRA_DOMAINS"), ","), pass, key)
	admin.WriteJSON(w, http.StatusOK, map[string]string{"link": link})
}
