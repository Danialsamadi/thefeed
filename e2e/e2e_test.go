//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/sartoopjj/thefeed/internal/controlapi"
	"github.com/sartoopjj/thefeed/internal/protocol"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// Where install.sh puts thefeed, and where `make e2e` leaves its builds. The
// suite runs as root from this directory.
const (
	installDir = "/opt/thefeed"
	dataDir    = installDir + "/data"
	adminDir   = installDir + "/admin"
	panelURL   = "https://127.0.0.1" // the panel's default :443, self-signed
	buildDir   = "../build/e2e"
	password   = "e2e-correct-horse-battery"
)

func TestE2E(t *testing.T) {
	preflight(t)
	build, err := filepath.Abs(buildDir)
	if err != nil {
		t.Fatal(err)
	}
	// make e2e builds the installed server and panel at one version, and the
	// server the fake release offers at a newer one.
	asset := "thefeed-server-linux-" + runtime.GOARCH
	from := version(t, filepath.Join(build, asset))
	to := version(t, filepath.Join(build, "release", asset))
	bin, err := os.ReadFile(filepath.Join(build, "release", asset))
	if err != nil {
		t.Fatal(err)
	}
	release := httptest.NewServer(fakeRelease(to, map[string][]byte{asset: bin}))
	t.Cleanup(release.Close)

	install(t, build, release.URL)
	s := &suite{
		p:      newPanel(),
		domain: envValue(t, "THEFEED_DOMAIN"),
		key:    envValue(t, "THEFEED_KEY"),
		listen: envValue(t, "THEFEED_LISTEN"),
		ctl:    controlapi.New(filepath.Join(dataDir, controlapi.SocketName)),
	}
	eventually(t, time.Minute, "the panel answers", func() (bool, string) {
		code, raw, err := s.p.do(http.MethodGet, "/api/auth/setup", nil)
		return code == http.StatusOK, fmt.Sprintf("%d %s %v", code, raw, err)
	})
	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"first run", s.firstRun},
		{"restart", s.restart},
		{"public channel", s.publicChannel},
		{"answering", s.answering},
		{"stop and start", s.stopStart},
		{"update and rollback", func(t *testing.T) { s.update(t, from, to) }},
		{"lockout", s.lockout},
	} {
		if !t.Run(step.name, step.run) {
			t.FailNow() // each flow starts where the one before left off
		}
	}
}

// preflight refuses anything but a disposable Linux host with systemd: the
// suite installs into /opt/thefeed and deletes it afterwards.
func preflight(t *testing.T) {
	switch {
	case runtime.GOOS != "linux":
		t.Fatal("the e2e suite needs Linux with systemd")
	case os.Geteuid() != 0:
		t.Fatal("the e2e suite needs root: run it with make e2e")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Fatal("systemd isn't running here (no /run/systemd/system)")
	}
	for _, p := range []string{installDir, "/etc/systemd/system/thefeed-server.service", "/etc/systemd/system/thefeed-admin.service"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s exists: the suite won't install over thefeed, or delete it afterwards", p)
		}
	}
}

// install runs install.sh --local over testdata/thefeed.env plus the fake
// release's address, and uninstalls when the test ends.
func install(t *testing.T, build, release string) {
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("journalctl", "-u", "thefeed-server", "-u", "thefeed-admin", "--no-pager", "-n", "400").CombinedOutput()
			t.Logf("journal:\n%s", out)
		}
		if out, err := exec.Command("bash", "../scripts/install.sh", "--uninstall").CombinedOutput(); err != nil {
			t.Errorf("install.sh --uninstall: %v\n%s", err, out)
		}
		os.RemoveAll(installDir) // preflight saw none before
	})
	seed, err := os.ReadFile("testdata/thefeed.env")
	if err == nil {
		err = os.MkdirAll(dataDir, 0o700)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dataDir, "thefeed.env"), append(seed, "THEFEED_ADMIN_RELEASE_BASE_URL="+release+"\n"...), 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "../scripts/install.sh", "--local", build)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root"}
	// No controlling terminal, so install.sh can't reopen /dev/tty: each
	// prompt reads the empty stdin and keeps thefeed.env's value.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --local: %v\n%s", err, out)
	}
	t.Logf("install.sh --local:\n%s", out)
}

type suite struct {
	p                   *panel
	domain, key, listen string
	ctl                 controlapi.Client
}

// 1. The admin is made with the setup code from the journal, then logs in.
func (s *suite) firstRun(t *testing.T) {
	var setup struct{ Needed bool }
	s.p.must(t, http.MethodGet, "/api/auth/setup", nil, &setup)
	if !setup.Needed {
		t.Fatal("a fresh install already has an admin")
	}
	code := setupCode(t)
	s.p.must(t, http.MethodPost, "/api/auth/setup", map[string]string{"code": code, "username": "admin", "password": password, "repeat": password}, nil)
	s.p.must(t, http.MethodPost, "/api/auth/logout", nil, nil)
	if code := s.p.call(t, http.MethodGet, "/api/auth/session", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("session after logout: %d, want 401", code)
	}
	s.p.must(t, http.MethodPost, "/api/auth/login", login(password), nil)
	var me struct{ Username string }
	s.p.must(t, http.MethodGet, "/api/auth/session", nil, &me)
	if me.Username != "admin" {
		t.Fatalf("signed in as %q, want admin", me.Username)
	}
}

var setupCodeRE = regexp.MustCompile(`admin: setup code (\S+)`)

func setupCode(t *testing.T) string {
	t.Helper()
	var code string
	eventually(t, 30*time.Second, "the setup code in the journal", func() (bool, string) {
		out, err := exec.Command("journalctl", "-u", "thefeed-admin", "--no-pager", "-o", "cat").CombinedOutput()
		m := setupCodeRE.FindAllSubmatch(out, -1)
		if len(m) == 0 {
			return false, fmt.Sprintf("%v %s", err, out)
		}
		code = string(m[len(m)-1][1])
		return true, ""
	})
	if file, _ := os.ReadFile(filepath.Join(adminDir, "setup-code")); strings.TrimSpace(string(file)) != code {
		t.Fatalf("the journal's setup code is %s, %s/setup-code has %q", code, adminDir, file)
	}
	return code
}

// 2. A restart-only setting waits until the restart, which starts a new
// process that runs it.
func (s *suite) restart(t *testing.T) {
	var st settingsState
	s.p.must(t, http.MethodGet, "/api/settings", nil, &st)
	if v := st.get(t, "fetch.msgs"); v.Running == "" || v.Running == "20" {
		t.Fatalf("fetch.msgs before: %+v, want it running at something other than 20", v)
	}
	pid := mainPID(t)
	s.p.must(t, http.MethodPatch, "/api/settings/fetch.msgs", map[string]any{"value": "20", "base": st.Base}, &st)
	if v := st.get(t, "fetch.msgs"); v.Saved != "20" || !v.Waiting {
		t.Fatalf("fetch.msgs after saving 20: %+v, want it waiting for a restart", v)
	}
	s.act(t, "restart", nil, "running")
	eventually(t, time.Minute, "/status runs fetch.msgs 20", func() (bool, string) {
		st, err := s.status()
		if err != nil {
			return false, err.Error()
		}
		return st.Server.Config["fetch.msgs"] == "20", "fetch.msgs " + st.Server.Config["fetch.msgs"]
	})
	if now := mainPID(t); now == pid || now == "0" {
		t.Fatalf("thefeed-server's PID was %s before the restart and is %s after", pid, now)
	}
	s.p.must(t, http.MethodGet, "/api/settings", nil, &st)
	if v := st.get(t, "fetch.msgs"); v.Running != "20" || v.Waiting {
		t.Fatalf("fetch.msgs after the restart: %+v, want running 20", v)
	}
}

// 3. A public channel applies live: the server reloads it, no restart.
func (s *suite) publicChannel(t *testing.T) {
	pid := mainPID(t)
	var st settingsState
	s.p.must(t, http.MethodGet, "/api/settings", nil, &st)
	var res struct {
		settingsState
		Live        bool
		ReloadError string
	}
	s.p.must(t, http.MethodPost, "/api/lists/channels", map[string]any{"op": "add", "value": "durov", "base": st.Base}, &res)
	if !res.Live || res.ReloadError != "" {
		t.Fatalf("adding @durov: live %v, reload error %q", res.Live, res.ReloadError)
	}
	if !slices.ContainsFunc(res.Lists["channels"].Items, func(it listItem) bool { return it.Name == "durov" && !it.Waiting }) {
		t.Fatalf("@durov isn't running: %+v", res.Lists["channels"].Items)
	}
	if now := mainPID(t); now != pid {
		t.Fatalf("thefeed-server restarted (PID %s, now %s); public channels apply live", pid, now)
	}
}

// 4. Stopping answers silences a real feed query, survives a panel
// restart, and resuming brings answers back.
func (s *suite) answering(t *testing.T) {
	if err := s.query(); err != nil {
		t.Fatalf("a feed query while answering: %v", err)
	}
	s.act(t, "pause", nil, "paused")
	s.unanswered(t)
	run(t, "systemctl", "restart", "thefeed-admin")
	eventually(t, time.Minute, "the restarted panel still shows it paused", func() (bool, string) {
		code, raw, err := s.p.do(http.MethodGet, "/api/service", nil)
		var sv service
		json.Unmarshal(raw, &sv)
		return code == http.StatusOK && sv.State == "paused", fmt.Sprintf("%d %s %v", code, raw, err)
	})
	s.unanswered(t)
	s.act(t, "resume", nil, "running")
	if err := s.query(); err != nil {
		t.Fatalf("a feed query after resuming: %v", err)
	}
}

// 5. Stop and start, each seen on the Board.
func (s *suite) stopStart(t *testing.T) {
	s.act(t, "stop", nil, "stopped")
	s.board(t, "stopped")
	s.act(t, "start", nil, "running")
	s.board(t, "running")
	eventually(t, 30*time.Second, "a feed query answered", func() (bool, string) {
		err := s.query()
		return err == nil, fmt.Sprint(err)
	})
}

// 6. Update to the fake release, checked against its SHA256SUMS, then roll
// back to the version it replaced.
func (s *suite) update(t *testing.T, from, to string) {
	var sv service
	s.p.must(t, http.MethodGet, "/api/service", nil, &sv)
	if sv.Version != from || sv.Latest != to || !sv.Update {
		t.Fatalf("service: %+v, want %s installed and %s offered", sv, from, to)
	}
	sv = s.act(t, "update", map[string]string{"version": to}, "running")
	if sv.Version != to || sv.Previous != from {
		t.Fatalf("after the update: %+v, want %s running and %s kept", sv, to, from)
	}
	if v := version(t, filepath.Join(installDir, "thefeed-server.previous")); v != from {
		t.Fatalf("thefeed-server.previous is %s, want %s", v, from)
	}
	sv = s.act(t, "rollback", nil, "running")
	if sv.Previous != "" {
		t.Fatalf("after the rollback: %+v, want nothing left to roll back to", sv)
	}
	eventually(t, time.Minute, "/status reports "+from+" again", func() (bool, string) {
		st, err := s.status()
		if err != nil {
			return false, err.Error()
		}
		return st.Server.Version == from, st.Server.Version
	})
	if v := version(t, filepath.Join(installDir, "thefeed-server")); v != from {
		t.Fatalf("the installed thefeed-server is %s after the rollback, want %s", v, from)
	}
}

// 7. Five wrong passwords lock sign-in, even for the right one.
func (s *suite) lockout(t *testing.T) {
	s.p.must(t, http.MethodPost, "/api/auth/logout", nil, nil)
	var e struct{ TriesLeft, RetryAfter int }
	for left := 4; left >= 1; left-- {
		if code := s.p.call(t, http.MethodPost, "/api/auth/login", login("wrong password"), &e); code != http.StatusUnauthorized || e.TriesLeft != left {
			t.Fatalf("a wrong password: %d with %d tries left, want 401 with %d", code, e.TriesLeft, left)
		}
	}
	if code := s.p.call(t, http.MethodPost, "/api/auth/login", login("wrong password"), &e); code != http.StatusTooManyRequests || e.RetryAfter <= 0 {
		t.Fatalf("the fifth wrong password: %d %+v, want 429 and a wait", code, e)
	}
	if code := s.p.call(t, http.MethodPost, "/api/auth/login", login(password), nil); code != http.StatusTooManyRequests {
		t.Fatalf("the right password while locked: %d, want 429", code)
	}
	if code := s.p.call(t, http.MethodGet, "/api/auth/session", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("session while locked out: %d, want 401", code)
	}
}

func login(pass string) map[string]string {
	return map[string]string{"username": "admin", "password": pass}
}

// --- the panel's API ---

type service struct {
	State, Version, Previous, Latest string
	Update                           bool
}

type settingsState struct {
	Base     map[string]string
	Settings []setting
	Lists    map[string]struct{ Items []listItem }
}

type setting struct {
	Key, Saved, Running string
	Waiting             bool
}

type listItem struct {
	Name    string
	Waiting bool
}

func (st settingsState) get(t *testing.T, key string) setting {
	t.Helper()
	for _, s := range st.Settings {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("no setting %s", key)
	return setting{}
}

// act posts a Service action and checks the state it answers with.
func (s *suite) act(t *testing.T, action string, body any, want string) service {
	t.Helper()
	var sv service
	s.p.must(t, http.MethodPost, "/api/service/"+action, body, &sv)
	if sv.State != want {
		t.Fatalf("after %s the server is %q, want %q", action, sv.State, want)
	}
	return sv
}

// board waits for the Board to show the server in state; it samples
// /status once a minute.
func (s *suite) board(t *testing.T, state string) {
	t.Helper()
	eventually(t, 2*time.Minute, "the Board shows the server "+state, func() (bool, string) {
		var b struct{ Server struct{ State string } }
		s.p.must(t, http.MethodGet, "/api/board", nil, &b)
		return b.Server.State == state, b.Server.State
	})
}

type panel struct{ c *http.Client }

func newPanel() *panel {
	jar, _ := cookiejar.New(nil)
	return &panel{&http.Client{Jar: jar, Timeout: 3 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		// Self-signed, so no CA vouches for it: it has to be the one the
		// panel wrote to its admin directory.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			data, err := os.ReadFile(filepath.Join(adminDir, "tls", "cert.pem"))
			if err != nil {
				return err
			}
			if b, _ := pem.Decode(data); b == nil || len(raw) == 0 || !bytes.Equal(raw[0], b.Bytes) {
				return errors.New("not the certificate in " + adminDir + "/tls/cert.pem")
			}
			return nil
		},
	}}}}
}

// do sends a request as the panel's page does: anything but GET as JSON
// ({} without a body) with the panel's own Origin.
func (p *panel) do(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if method != http.MethodGet {
		if body == nil {
			body = struct{}{}
		}
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, panelURL+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", panelURL)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// call is do, decoding the answer into out (if not nil), failing the test
// when there's no answer.
func (p *panel) call(t *testing.T, method, path string, body, out any) int {
	t.Helper()
	code, raw, err := p.do(method, path, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %d %s: %v", method, path, code, raw, err)
		}
	}
	return code
}

// must is call, failing unless the panel answers 200.
func (p *panel) must(t *testing.T, method, path string, body, out any) {
	t.Helper()
	var raw json.RawMessage
	if code := p.call(t, method, path, body, &raw); code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	if out != nil {
		json.Unmarshal(raw, out)
	}
}

// --- the machine ---

func (s *suite) status() (*controlapi.Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.ctl.Status(ctx)
}

// query sends the feed's first query, as readers and the Board's check do,
// and checks the answer opens with the passphrase.
func (s *suite) query() error {
	qk, rk, err := protocol.DeriveKeys(s.key)
	if err != nil {
		return err
	}
	name, err := protocol.EncodeQuery(qk, protocol.MetadataChannel, 0, s.domain, protocol.QuerySingleLabel)
	if err != nil {
		return err
	}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
	m.SetEdns0(4096, false)
	resp, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, s.listen)
	if err != nil {
		return err
	}
	if resp.Rcode != dns.RcodeSuccess {
		return fmt.Errorf("answered %s", dns.RcodeToString[resp.Rcode])
	}
	for _, rr := range resp.Answer {
		if txt, ok := rr.(*dns.TXT); ok {
			_, err := protocol.DecodeResponse(rk, strings.Join(txt.Txt, ""))
			return err
		}
	}
	return errors.New("the answer has no TXT record")
}

// unanswered checks a feed query gets no answer at all, not even an error.
func (s *suite) unanswered(t *testing.T) {
	t.Helper()
	var ne net.Error
	if err := s.query(); !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("a feed query while not answering: %v, want a timeout", err)
	}
}

func mainPID(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(run(t, "systemctl", "show", "--property=MainPID", "--value", "thefeed-server"))
}

// version is what a thefeed-server binary says it is.
func version(t *testing.T, bin string) string {
	t.Helper()
	f := strings.Fields(run(t, bin, "--version")) // thefeed-server v0.98.0 (commit: …)
	if len(f) < 2 {
		t.Fatalf("%s --version: %q", bin, f)
	}
	return f[1]
}

func envValue(t *testing.T, key string) string {
	t.Helper()
	data, _, err := serverconfig.ReadFile(filepath.Join(dataDir, "thefeed.env"))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := serverconfig.ParseEnv(data).Get(key)
	if !ok || v == "" {
		t.Fatalf("thefeed.env has no %s", key)
	}
	return v
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// eventually retries check each second until it holds, failing with its
// last word after limit.
func eventually(t *testing.T, limit time.Duration, what string, check func() (bool, string)) {
	t.Helper()
	for end := time.Now().Add(limit); ; time.Sleep(time.Second) {
		ok, last := check()
		if ok {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%s: not within %s (last: %s)", what, limit, last)
		}
	}
}
