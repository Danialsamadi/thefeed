package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/service"
	"github.com/sartoopjj/thefeed/internal/controlapi"
)

// machine fakes everything outside the panel: systemd, the running
// thefeed-server behind its control socket, GitHub's releases and the clock.
// Starting the server "runs" whatever is in the binary file: its bytes are
// the version it reports, and a binary that says "crash" never answers.
type machine struct {
	t                 *testing.T
	now               time.Time
	bin, unit, data   string
	cmds              []string // systemctl commands, without the state reads
	active, enabled   bool
	running           string // version the server reports; "" while it doesn't answer
	answering         bool
	started           time.Time
	controlapi.Client                   // nil: only Status and SetAnswering may be called
	latest            map[string]string // repo → newest tag
	lookups           int
	config            map[string]string // the running settings, as /status reports them
	assets            map[string][]byte // "tag/name" → bytes
}

const domain = "t.example.com"

var asset = "thefeed-server-linux-" + runtime.GOARCH

// newMachine is a server installed the way install.sh does it, running
// version, enabled at boot, started at 07:58.
func newMachine(t *testing.T, version string) *machine {
	t.Helper()
	dir := t.TempDir()
	m := &machine{t: t, now: time.Date(2026, 11, 4, 12, 4, 0, 0, time.UTC), bin: filepath.Join(dir, "thefeed-server"),
		unit: filepath.Join(dir, "thefeed-server.service"), data: filepath.Join(dir, "data"), enabled: true, assets: map[string][]byte{}, latest: map[string]string{},
		config: map[string]string{"dns.domain": domain, "server.listen": ":5300"}}
	if err := os.MkdirAll(m.data, 0o700); err != nil {
		t.Fatal(err)
	}
	m.write(m.bin, version, 0o755)
	m.write(filepath.Join(m.data, "thefeed.env"), "# thefeed-server settings\nTHEFEED_DOMAIN="+domain+"\nTHEFEED_LISTEN=:5300\n", 0o600)
	m.write(m.unit, "[Service]\nEnvironmentFile="+m.data+"/thefeed.env\nExecStart="+m.bin+" --data-dir "+m.data+"\nRestart=on-failure\n", 0o644)
	at := m.now
	m.now = time.Date(2026, 10, 22, 7, 58, 0, 0, time.UTC)
	m.boot()
	m.now = at
	return m
}

func (m *machine) write(path, content string, mode os.FileMode) {
	m.t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) read(path string) string {
	m.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		m.t.Fatal(err)
	}
	return string(b)
}

func (m *machine) boot() {
	b, _ := os.ReadFile(m.bin)
	m.active, m.answering, m.started, m.running = true, true, m.now, string(b)
	if m.running == "crash" {
		m.active, m.running = false, ""
	}
}

func (m *machine) Run(_ context.Context, args ...string) ([]byte, error) {
	if args[0] == "show" {
		state, file := "inactive", "disabled"
		if m.active {
			state = "active"
		}
		if m.enabled {
			file = "enabled"
		}
		return []byte("ActiveState=" + state + "\nUnitFileState=" + file + "\n"), nil
	}
	m.cmds = append(m.cmds, strings.Join(args, " "))
	switch args[0] {
	case "restart", "start":
		m.boot()
	case "stop":
		m.active, m.running = false, ""
	case "enable":
		m.enabled = true
	case "disable":
		m.enabled = false
	}
	return nil, nil
}

func (m *machine) Status(context.Context) (*controlapi.Status, error) {
	if !m.active || m.running == "" {
		return nil, errors.New("dial unix control.sock: connect: connection refused")
	}
	return &controlapi.Status{Server: controlapi.Server{Version: m.running, Started: m.started, Answering: m.answering,
		Config: m.config}}, nil
}

func (m *machine) SetAnswering(_ context.Context, on bool) error {
	if !m.active {
		return errors.New("dial unix control.sock: connect: connection refused")
	}
	m.answering = on
	return nil
}

func (m *machine) Latest(_ context.Context, repo string) (string, error) {
	m.lookups++
	if tag, ok := m.latest[repo]; ok {
		return tag, nil
	}
	return "", errors.New("latest release: expected 3xx, got 404 Not Found")
}

func (m *machine) Download(_ context.Context, repo, tag, name string) (io.ReadCloser, error) {
	b, ok := m.assets[repo+"/"+tag+"/"+name]
	if !ok {
		return nil, errors.New("404 Not Found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// publish puts a release on GitHub the way the release workflow does: the
// binary, here just its version, and SHA256SUMS over it.
func (m *machine) publish(repo, tag, binary string) {
	sum := sha256.Sum256([]byte(binary))
	m.assets[repo+"/"+tag+"/"+asset] = []byte(binary)
	m.assets[repo+"/"+tag+"/SHA256SUMS"] = []byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n")
	m.latest[repo] = tag
}

func (m *machine) sleep(_ context.Context, d time.Duration) { m.now = m.now.Add(d) }

type panel struct {
	t      *testing.T
	m      *machine
	app    *admin.App
	cookie *http.Cookie
}

const origin = "https://panel.test"

// pass is the admin's password; pw is the body of an action that asks for
// it again.
const pass = "correct horse battery"

var pw = map[string]string{"password": pass}

// newPanel is the real admin app with the service module on m, signed in.
// edits change its options, such as dropping the fake release source.
func newPanel(t *testing.T, m *machine, edits ...func(*service.Options)) *panel {
	t.Helper()
	cfg := admin.Config{AdminDir: t.TempDir(), ServerDataDir: m.data, Clock: admin.ClockFunc(func() time.Time { return m.now })}
	o := service.Options{Systemd: m, Control: m, Releases: m, Sleep: m.sleep, Binary: m.bin, Unit: m.unit}
	for _, e := range edits {
		e(&o)
	}
	app, err := admin.New(cfg, service.New(o))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	p := &panel{t: t, m: m, app: app}
	code := strings.TrimSpace(m.read(filepath.Join(cfg.AdminDir, "setup-code")))
	w := p.do("POST", "/api/auth/setup", map[string]string{"code": code, "username": "admin", "password": "correct horse battery", "repeat": "correct horse battery"})
	for _, c := range w.Result().Cookies() {
		if c.Name == "thefeed_admin" && c.Value != "" {
			p.cookie = c
		}
	}
	if p.cookie == nil {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	return p
}

func (p *panel) do(method, path string, body any) *httptest.ResponseRecorder {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, origin+path, r)
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
	}
	if p.cookie != nil {
		req.AddCookie(p.cookie)
	}
	w := httptest.NewRecorder()
	p.app.Handler().ServeHTTP(w, req)
	return w
}

type line struct {
	At   time.Time
	Text string
}

type state struct {
	State          string
	Since, Started time.Time
	Boot           bool
	Version        string
	Previous       string
	Latest         string
	Update         bool
	Source         string
	Domain         string
	Listen         string
	Last           *line
	Shadowing      []string
}

func (p *panel) state(w *httptest.ResponseRecorder) state {
	p.t.Helper()
	if w.Code != 200 {
		p.t.Fatalf("%d %s", w.Code, w.Body)
	}
	var s state
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		p.t.Fatalf("%v in %s", err, w.Body)
	}
	return s
}

// act runs a power action and wants it to work.
func (p *panel) act(action string, body any) state {
	p.t.Helper()
	if body == nil {
		body = struct{}{}
	}
	return p.state(p.do("POST", "/api/service/"+action, body))
}

// errorOf wants the panel to refuse with status and returns its message.
func (p *panel) errorOf(w *httptest.ResponseRecorder, status int) (field, message string) {
	p.t.Helper()
	var e struct {
		Errors []struct{ Field, Message string }
	}
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &e) != nil || len(e.Errors) == 0 {
		p.t.Fatalf("got %d %s, want %d with an error", w.Code, w.Body, status)
	}
	return e.Errors[0].Field, e.Errors[0].Message
}

// actions is the action record, newest first, as "Action: Outcome".
func (p *panel) actions() []string {
	p.t.Helper()
	var r struct{ Actions []admin.Action }
	if err := json.Unmarshal(p.do("GET", "/api/actions", nil).Body.Bytes(), &r); err != nil {
		p.t.Fatal(err)
	}
	var out []string
	for _, a := range r.Actions {
		out = append(out, a.Action+": "+a.Outcome)
	}
	return out
}

func (m *machine) wantCmds(want ...string) {
	m.t.Helper()
	if !slices.Equal(m.cmds, want) {
		m.t.Fatalf("systemctl got %q, want %q", m.cmds, want)
	}
}

func TestRestartRestartsTheServer(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)

	s := p.act("restart", nil)

	m.wantCmds("restart thefeed-server")
	if s.State != "running" || !s.Since.Equal(m.now) || s.Listen != ":5300" || s.Last == nil || s.Last.Text != "Restarted" || !s.Last.At.Equal(m.now) {
		t.Fatalf("after restart: %+v, want running on :5300 since now, last line Restarted", s)
	}
	if got := p.actions(); len(got) == 0 || got[0] != "Restart: Done" {
		t.Fatalf("action record = %q, want Restart: Done first", got)
	}
}

// Stop, start, start on boot and reboot, each as systemctl would be typed.
func TestEachPowerActionSendsItsCommand(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	for _, c := range []struct {
		action, cmd, state string
		boot               bool
	}{
		{"stop", "stop thefeed-server", "stopped", true},
		{"start", "start thefeed-server", "running", true},
		{"boot-off", "disable thefeed-server", "running", false},
		{"boot-on", "enable thefeed-server", "running", true},
		{"reboot", "reboot", "running", true},
	} {
		m.cmds = nil
		s := p.act(c.action, nil)
		m.wantCmds(c.cmd)
		if s.State != c.state || s.Boot != c.boot {
			t.Errorf("after %s: state %s, boot %v; want %s, %v", c.action, s.State, s.Boot, c.state, c.boot)
		}
	}
	want := []string{"Reboot: Done", "Start on boot on: Done", "Start on boot off: Done", "Start server: Done", "Stop server: Done"}
	if got := p.actions(); !slices.Equal(got, want) {
		t.Fatalf("action record = %q, want %q", got, want)
	}
}

// Stopping answering goes through the control socket; the process stays up.
func TestStopAnsweringThenResume(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	pausedAt := m.now

	s := p.act("pause", nil)
	if m.answering || !m.active || s.State != "paused" || !s.Since.Equal(pausedAt) || !s.Started.Equal(m.started) {
		t.Fatalf("after pause: answering %v, active %v, %+v; want paused since %s, started kept", m.answering, m.active, s, pausedAt)
	}
	m.now = m.now.Add(10 * time.Minute)
	if s := p.state(p.do("GET", "/api/service", nil)); s.State != "paused" || !s.Since.Equal(pausedAt) {
		t.Fatalf("10 minutes later: %+v, want still paused since %s", s, pausedAt)
	}

	s = p.act("resume", nil)
	if !m.answering || s.State != "running" || s.Last == nil || s.Last.Text != "Answering again" {
		t.Fatalf("after resume: answering %v, %+v; want running, last line Answering again", m.answering, s)
	}
	m.wantCmds()
	if got, want := p.actions(), []string{"Resume answering: Done", "Stop answering: Done"}; !slices.Equal(got, want) {
		t.Fatalf("action record = %q, want %q", got, want)
	}
}

// Only the hosting provider's console turns the machine on again, so the
// operator types the domain first. It comes from thefeed.env, so this works
// with the server stopped too.
func TestShutdownNeedsTheTypedDomain(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	p.act("stop", nil)
	m.cmds = nil
	if s := p.state(p.do("GET", "/api/service", nil)); s.Domain != domain {
		t.Fatalf("domain = %q, want %q to type", s.Domain, domain)
	}

	for _, typed := range []string{"", "t.example.co", "example.com"} {
		field, msg := p.errorOf(p.do("POST", "/api/service/shutdown", map[string]string{"domain": typed}), http.StatusBadRequest)
		if field != "domain" || !strings.Contains(msg, domain) {
			t.Errorf("typed %q: %s %q, want an error on domain that names %s", typed, field, msg, domain)
		}
	}
	m.wantCmds()

	p.act("shutdown", map[string]string{"domain": " " + domain + " "})
	m.wantCmds("poweroff")
	if got := p.actions(); got[0] != "Shut down: Done" {
		t.Fatalf("action record = %q, want Shut down: Done first", got)
	}
}

// Releases come from the repo the installer recorded, so a fork updates
// from itself. GitHub is asked at most once an hour.
func TestStateOffersTheNewerRelease(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	m.write(filepath.Join(m.data, "thefeed.env"), "THEFEED_DOMAIN="+domain+"\nTHEFEED_ADMIN_RELEASE_REPO=fork/thefeed\n", 0o600)
	m.publish("sartoopjj/thefeed", "v0.42.0", "v0.42.0")
	m.publish("fork/thefeed", "v0.41.1", "v0.41.1")
	p := newPanel(t, m)

	s := p.state(p.do("GET", "/api/service", nil))
	if s.Version != "v0.41.0" || s.Latest != "v0.41.1" || !s.Update || s.Source != "fork/thefeed" {
		t.Fatalf("state = %+v, want v0.41.0 installed, v0.41.1 from fork/thefeed available", s)
	}
	m.now = m.now.Add(59 * time.Minute)
	p.do("GET", "/api/service", nil)
	if m.lookups != 1 {
		t.Fatalf("GitHub asked %d times within the hour, want once", m.lookups)
	}

	m.publish("fork/thefeed", "v0.41.0", "v0.41.0")
	m.now = m.now.Add(2 * time.Minute)
	if s := p.state(p.do("GET", "/api/service", nil)); s.Latest != "v0.41.0" || s.Update {
		t.Fatalf("an hour later = %+v, want v0.41.0 is the newest, no update", s)
	}
}

// A download that doesn't match the release's SHA256SUMS, or one the file
// doesn't list, installs nothing and restarts nothing.
func TestUpdateWithoutAMatchingChecksumIsRefused(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	m.publish("sartoopjj/thefeed", "v0.41.1", "v0.41.1")
	m.assets["sartoopjj/thefeed/v0.41.1/"+asset] = []byte("v0.41.1 with something extra")

	_, msg := p.errorOf(p.do("POST", "/api/service/update", pw), http.StatusBadGateway)
	if !strings.Contains(msg, "SHA256SUMS") {
		t.Errorf("error = %q, want it to name SHA256SUMS", msg)
	}

	m.assets["sartoopjj/thefeed/v0.41.1/SHA256SUMS"] = []byte("0f343b0931126a20f133d67c2b018a3b2a8f1d4f7b0b2a4bfc3c1d0f5a1e2b3c  thefeed-admin-linux-amd64\n")
	_, msg = p.errorOf(p.do("POST", "/api/service/update", pw), http.StatusBadGateway)
	if !strings.Contains(msg, asset) {
		t.Errorf("error = %q, want it to name %s", msg, asset)
	}

	m.wantCmds()
	if got := m.read(m.bin); got != "v0.41.0" {
		t.Fatalf("binary = %q, want v0.41.0 untouched", got)
	}
	if _, err := os.Stat(m.bin + ".new"); err == nil {
		t.Fatalf("left the download behind")
	}
	if got := p.actions(); len(got) != 2 || !strings.HasPrefix(got[0], "Update to v0.41.1: ") {
		t.Fatalf("action record = %q, want both refused updates", got)
	}
}

// The current binary is kept as thefeed-server.previous, the new one takes
// its place, and the server restarts on it.
func TestUpdateSwapsBinariesAndRestarts(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	m.publish("sartoopjj/thefeed", "v0.41.1", "v0.41.1")

	s := p.act("update", pw)

	m.wantCmds("restart thefeed-server")
	if got, prev := m.read(m.bin), m.read(m.bin+".previous"); got != "v0.41.1" || prev != "v0.41.0" {
		t.Fatalf("binary %q, previous %q; want v0.41.1, v0.41.0", got, prev)
	}
	if fi, _ := os.Stat(m.bin); fi.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode %v, want 0755", fi.Mode().Perm())
	}
	if m.running != "v0.41.1" || s.Version != "v0.41.1" || s.Previous != "v0.41.0" || s.Update || s.Last == nil || s.Last.Text != "Updated to v0.41.1" {
		t.Fatalf("running %q, state %+v; want v0.41.1 with v0.41.0 to go back to", m.running, s)
	}
	if got := p.actions(); got[0] != "Update to v0.41.1: Done" {
		t.Fatalf("action record = %q, want Update to v0.41.1: Done first", got)
	}
}

// A release that doesn't answer healthy within 60 seconds is swapped back
// out on its own, and the panel says so.
func TestUnhealthyUpdateRollsBack(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	m.publish("sartoopjj/thefeed", "v0.41.1", "crash")
	start := m.now

	_, msg := p.errorOf(p.do("POST", "/api/service/update", pw), http.StatusBadGateway)

	if !strings.Contains(msg, "60 seconds") || !strings.Contains(msg, "v0.41.0") {
		t.Errorf("error = %q, want it to say v0.41.1 wasn't healthy in 60 seconds and v0.41.0 is back", msg)
	}
	if m.now.Sub(start) < time.Minute {
		t.Errorf("gave up after %s, want a minute", m.now.Sub(start))
	}
	m.wantCmds("restart thefeed-server", "restart thefeed-server")
	if got := m.read(m.bin); got != "v0.41.0" || m.running != "v0.41.0" {
		t.Fatalf("binary %q, running %q; want v0.41.0 back", got, m.running)
	}
	s := p.state(p.do("GET", "/api/service", nil))
	if s.Version != "v0.41.0" || s.Previous != "" || s.Last == nil || !strings.Contains(s.Last.Text, "rolled back") {
		t.Fatalf("state = %+v, want v0.41.0, nothing to roll back to, and a line saying it rolled back", s)
	}
	if got := p.actions(); !strings.Contains(got[0], "Update to v0.41.1: ") || !strings.Contains(got[0], "rolled back") {
		t.Fatalf("action record = %q, want the update and its rollback first", got)
	}
}

func TestRollBackPutsThePreviousVersionBack(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	p.errorOf(p.do("POST", "/api/service/rollback", pw), http.StatusConflict)
	m.publish("sartoopjj/thefeed", "v0.41.1", "v0.41.1")
	p.act("update", pw)
	m.cmds = nil

	s := p.act("rollback", pw)

	m.wantCmds("restart thefeed-server")
	if got := m.read(m.bin); got != "v0.41.0" || m.running != "v0.41.0" {
		t.Fatalf("binary %q, running %q; want v0.41.0 back", got, m.running)
	}
	if s.Version != "v0.41.0" || s.Previous != "" || !s.Update || s.Last == nil || s.Last.Text != "Rolled back to v0.41.0" {
		t.Fatalf("state = %+v, want v0.41.0, v0.41.1 on offer again, nothing to roll back to", s)
	}
	if got := p.actions(); got[0] != "Roll back to v0.41.0: Done" {
		t.Fatalf("action record = %q, want Roll back to v0.41.0: Done first", got)
	}
}

// Servers before v0.40.1-rc0 don't read every setting from thefeed.env, so
// the panel neither updates nor rolls back to one. A newest release that
// isn't a version installs nothing either.
func TestNoUpdateOrRollbackBelowTheFloor(t *testing.T) {
	m := newMachine(t, "v0.40.0")
	p := newPanel(t, m)
	for _, c := range []struct {
		latest string
		code   int
		want   string
	}{
		{"v0.39.2", http.StatusConflict, "The newest release, v0.39.2, is older than v0.40.1-rc0"},
		{"v0.40.0", http.StatusConflict, "The newest release, v0.40.0, is older than v0.40.1-rc0"},
		{"../../evil", http.StatusBadGateway, `Couldn't find the newest release of sartoopjj/thefeed: "../../evil" isn't a release version`},
	} {
		m.latest["sartoopjj/thefeed"] = c.latest
		if _, msg := p.errorOf(p.do("POST", "/api/service/update", pw), c.code); !strings.HasPrefix(msg, c.want) {
			t.Errorf("update with %q the newest: %q, want %q", c.latest, msg, c.want)
		}
	}
	m.wantCmds()

	m.publish("sartoopjj/thefeed", "v0.41.0", "v0.41.0")
	if s := p.act("update", pw); s.Previous != "v0.40.0" {
		t.Fatalf("previous = %q, want v0.40.0", s.Previous)
	}
	m.cmds = nil
	_, msg := p.errorOf(p.do("POST", "/api/service/rollback", pw), http.StatusConflict)
	if !strings.Contains(msg, "v0.40.1-rc0") {
		t.Errorf("rollback refused with %q, want it to name v0.40.1-rc0", msg)
	}
	m.wantCmds()
	if got := m.read(m.bin); got != "v0.41.0" {
		t.Fatalf("binary = %q, want v0.41.0 kept", got)
	}
}

// A flag in the unit beats thefeed.env, so an edit made in the panel would
// silently not apply. --data-dir stays, a ${VAR} value comes from the file
// anyway, and flags the registry doesn't know aren't settings.
func TestShadowingFlagsInTheUnitAreFound(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	if s := p.state(p.do("GET", "/api/service", nil)); len(s.Shadowing) != 0 {
		t.Fatalf("installer's unit: shadowing %q, want none", s.Shadowing)
	}

	m.write(m.unit, "[Service]\nExecStart="+m.bin+" --data-dir "+m.data+" --domain t.example.com -key=abc \\\n"+
		"    --listen :5300 --debug --x-accounts ${THEFEED_X_ACCOUNTS} --env-file /etc/thefeed.env\nRestart=on-failure\n", 0o644)
	s := p.state(p.do("GET", "/api/service", nil))
	if want := []string{"--domain", "--key", "--listen", "--debug"}; !slices.Equal(s.Shadowing, want) {
		t.Fatalf("shadowing = %q, want %q", s.Shadowing, want)
	}
}

// Fixing moves each flag into thefeed.env with the value the server runs
// with (secrets aren't in /status, so theirs is the flag's), leaves only the
// other words in ExecStart, keeps the old unit, and restarts.
func TestFixingTheUnitMovesItsFlagsIntoThefeedEnv(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	old := "[Service]\nExecStart=" + m.bin + " --data-dir " + m.data + " --domain t.example.com -key=abc \\\n" +
		"    --debug --env-file /etc/thefeed.env\nRestart=on-failure\n"
	m.write(m.unit, old, 0o644)
	m.config["dns.debug"] = "false" // as if THEFEED_DEBUG=false in the file won over --debug

	s := p.act("fix-unit", nil)

	m.wantCmds("daemon-reload", "restart thefeed-server")
	if got, want := m.read(m.unit), "[Service]\nExecStart="+m.bin+" --data-dir "+m.data+" --env-file /etc/thefeed.env\nRestart=on-failure\n"; got != want {
		t.Errorf("unit =\n%s\nwant\n%s", got, want)
	}
	if got := m.read(filepath.Join(m.data, "thefeed-server.service.bak")); got != old {
		t.Errorf("backup =\n%s\nwant the old unit", got)
	}
	if got, want := m.read(filepath.Join(m.data, "thefeed.env")), "# thefeed-server settings\nTHEFEED_DOMAIN="+domain+"\nTHEFEED_LISTEN=:5300\nTHEFEED_KEY=abc\nTHEFEED_DEBUG=false\n"; got != want {
		t.Errorf("thefeed.env =\n%s\nwant\n%s", got, want)
	}
	if len(s.Shadowing) != 0 || s.Last == nil || s.Last.Text != "Moved --domain, --key, --debug into thefeed.env and restarted" {
		t.Fatalf("state = %+v, want no shadowing flags and a line saying what moved", s)
	}
	if got := p.actions(); got[0] != "Move the unit's flags into thefeed.env: Done" {
		t.Fatalf("action record = %q", got)
	}
	p.errorOf(p.do("POST", "/api/service/fix-unit", struct{}{}), http.StatusConflict)
}

// The release source is the panel's own setting; empty goes back to the
// default.
func TestReleaseSourceCanBeChanged(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	m.publish("fork/thefeed", "v0.41.5", "v0.41.5")
	if s := p.state(p.do("GET", "/api/service", nil)); s.Source != "sartoopjj/thefeed" || s.Latest != "" {
		t.Fatalf("state = %+v, want upstream with no release found", s)
	}

	for _, bad := range []string{"fork", "https://github.com/fork/thefeed", "fork/the feed", "a/b/c"} {
		if field, _ := p.errorOf(p.do("POST", "/api/service/source", map[string]string{"repo": bad, "password": pass}), http.StatusBadRequest); field != "repo" {
			t.Errorf("repo %q: error on %q, want repo", bad, field)
		}
	}
	if s := p.act("source", map[string]string{"repo": " fork/thefeed ", "password": pass}); s.Source != "fork/thefeed" || s.Latest != "v0.41.5" || !s.Update {
		t.Fatalf("state = %+v, want fork/thefeed offering v0.41.5", s)
	}
	if s := p.act("source", map[string]string{"repo": "", "password": pass}); s.Source != "sartoopjj/thefeed" {
		t.Fatalf("source = %q after clearing it, want the default", s.Source)
	}
	if got, want := p.actions(), []string{"Release source: sartoopjj/thefeed", "Release source: fork/thefeed"}; !slices.Equal(got, want) {
		t.Fatalf("action record = %q, want %q", got, want)
	}
}

// A session cookie alone can't change what runs as root: updating, rolling
// back and changing the release source each take the password again, and
// wrong ones count toward the sign-in lockout.
func TestRootLevelActionsNeedThePassword(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	m.publish("sartoopjj/thefeed", "v0.41.1", "v0.41.1")

	for _, a := range []string{"update", "rollback", "source"} {
		for _, try := range []string{"", "not it at all"} {
			if field, _ := p.errorOf(p.do("POST", "/api/service/"+a, map[string]string{"repo": "evil/thefeed", "password": try}), http.StatusForbidden); field != "password" {
				t.Errorf("%s with password %q: error on %q, want password", a, try, field)
			}
		}
	}
	m.wantCmds()
	if s := p.state(p.do("GET", "/api/service", nil)); s.Source != "sartoopjj/thefeed" || s.Version != "v0.41.0" {
		t.Fatalf("state = %+v, want nothing changed", s)
	}
	// Three wrong passwords so far (an empty one isn't a try); two more lock
	// signing in too.
	p.errorOf(p.do("POST", "/api/service/update", map[string]string{"password": "not it at all"}), http.StatusForbidden)
	p.errorOf(p.do("POST", "/api/service/update", map[string]string{"password": "not it at all"}), http.StatusTooManyRequests)
	p.errorOf(p.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": pass}), http.StatusTooManyRequests)
}

func TestStoppedServerSaysSinceWhen(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	p := newPanel(t, m)
	stoppedAt := m.now
	p.act("stop", nil)
	m.now = m.now.Add(time.Hour)
	if s := p.state(p.do("GET", "/api/service", nil)); s.State != "stopped" || !s.Since.Equal(stoppedAt) {
		t.Fatalf("an hour after stopping: %+v, want stopped since %s", s, stoppedAt)
	}
}

// Readers add the server with one link: domain, key and server key, as
// thefeed-server --print-config prints it. It holds the key, so it stays out
// of the action record.
func TestImportLinkIsWhatPrintConfigPrints(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	env := filepath.Join(m.data, "thefeed.env")
	m.write(env, "THEFEED_DOMAIN="+domain+"\nTHEFEED_EXTRA_DOMAINS=a.example.com,b.example.com\nTHEFEED_KEY=s3cret-pass\n", 0o600)
	m.write(filepath.Join(m.data, "server_ed25519.key"), "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=\n", 0o600)
	p := newPanel(t, m)

	w := p.do("GET", "/api/service/import-link", nil)
	var got struct{ Link string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("GET import-link = %d %s", w.Code, w.Body)
	}
	if want := "thefeed://t.example.com/s3cret-pass?sk=A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg&d=a.example.com%2Cb.example.com&r=1.1.1.1%2C8.8.8.8"; got.Link != want {
		t.Fatalf("link = %q, want %q", got.Link, want)
	}
	if got := p.actions(); len(got) != 0 {
		t.Fatalf("action record = %q, want nothing", got)
	}

	m.write(env, "THEFEED_DOMAIN="+domain+"\n", 0o600)
	p.errorOf(p.do("GET", "/api/service/import-link", nil), http.StatusConflict)
}

// THEFEED_ADMIN_RELEASE_BASE_URL points the real release source somewhere
// other than github.com, laid out the same way: the end-to-end suite updates
// from a local fake release like this one.
func TestReleasesCanComeFromAnotherAddress(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	m.publish("fork/thefeed", "v0.41.1", "v0.41.1")
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fork/thefeed/releases/latest" {
			http.Redirect(w, r, "/fork/thefeed/releases/tag/v0.41.1", http.StatusFound)
			return
		}
		tag, name, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/fork/thefeed/releases/download/"), "/")
		b, ok := m.assets["fork/thefeed/"+tag+"/"+name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer gh.Close()
	m.write(filepath.Join(m.data, "thefeed.env"), "THEFEED_DOMAIN="+domain+"\nTHEFEED_ADMIN_RELEASE_REPO=fork/thefeed\nTHEFEED_ADMIN_RELEASE_BASE_URL="+gh.URL+"\n", 0o600)
	p := newPanel(t, m, func(o *service.Options) { o.Releases = nil })

	if s := p.state(p.do("GET", "/api/service", nil)); s.Latest != "v0.41.1" || !s.Update {
		t.Fatalf("state = %+v, want v0.41.1 found at %s", s, gh.URL)
	}
	p.act("update", pw)
	if got := m.read(m.bin); got != "v0.41.1" || m.running != "v0.41.1" {
		t.Fatalf("binary %q, running %q; want v0.41.1 from %s", got, m.running, gh.URL)
	}
}
