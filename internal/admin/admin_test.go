package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
)

const origin = "https://panel.test"

// panel is a real App on a temp dir with a clock the test moves.
type panel struct {
	t   *testing.T
	cfg admin.Config
	now time.Time
	app *admin.App
}

func newPanel(t *testing.T, modules ...admin.Module) *panel {
	t.Helper()
	p := &panel{t: t, now: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	p.cfg = admin.Config{AdminDir: t.TempDir(), Clock: admin.ClockFunc(func() time.Time { return p.now })}
	p.open(modules...)
	return p
}

func (p *panel) open(modules ...admin.Module) {
	p.t.Helper()
	app, err := admin.New(p.cfg, modules...)
	if err != nil {
		p.t.Fatal(err)
	}
	p.app = app
	p.t.Cleanup(func() { app.Close() })
}

// do sends a request the way the panel's own page would: JSON with a
// matching Origin on anything that changes state.
func (p *panel) do(method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, origin+path, strings.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
	}
	if c != nil {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	p.app.Handler().ServeHTTP(w, req)
	return w
}

// setupCode reads the code the way the installer does: from the file.
func (p *panel) setupCode() string {
	b, err := os.ReadFile(filepath.Join(p.cfg.AdminDir, "setup-code"))
	if err != nil {
		p.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func setupBody(code, user, pass, repeat string) string {
	b, _ := json.Marshal(map[string]string{"code": code, "username": user, "password": pass, "repeat": repeat})
	return string(b)
}

func loginBody(user, pass string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	return string(b)
}

const pass = "correct horse battery"

// signUp creates the admin and returns the session cookie.
func (p *panel) signUp() *http.Cookie {
	p.t.Helper()
	w := p.do("POST", "/api/auth/setup", setupBody(p.setupCode(), "admin", pass, pass), nil)
	if w.Code != 200 {
		p.t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	return cookieOf(p.t, w)
}

func (p *panel) login(password string) *httptest.ResponseRecorder {
	return p.do("POST", "/api/auth/login", loginBody("admin", password), nil)
}

func cookieOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == "thefeed_admin" && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no session cookie in %v", w.Header())
	return nil
}

// fieldErrors maps each error's field to its message.
func fieldErrors(t *testing.T, w *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body struct{ Errors []admin.FieldError }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad error body %q: %v", w.Body, err)
	}
	out := map[string]string{}
	for _, e := range body.Errors {
		out[e.Field] = e.Message
	}
	return out
}

func TestFirstRunSetup(t *testing.T) {
	p := newPanel(t)
	code := p.setupCode()
	if len(code) != 9 || code[4] != '-' {
		t.Fatalf("setup code %q, want XXXX-XXXX", code)
	}
	if w := p.do("GET", "/api/auth/setup", "", nil); !strings.Contains(w.Body.String(), `"needed":true`) {
		t.Fatalf("setup status = %s", w.Body)
	}

	w := p.do("POST", "/api/auth/setup", setupBody("AAAA-AAAA", "admin", pass, pass), nil)
	if w.Code != 400 || fieldErrors(t, w)["code"] == "" {
		t.Fatalf("wrong code = %d %s", w.Code, w.Body)
	}
	w = p.do("POST", "/api/auth/setup", setupBody(code, "admin", "short", "short"), nil)
	if w.Code != 400 || fieldErrors(t, w)["password"] == "" {
		t.Fatalf("short password = %d %s", w.Code, w.Body)
	}
	w = p.do("POST", "/api/auth/setup", setupBody(code, "admin", pass, pass+"x"), nil)
	if w.Code != 400 || fieldErrors(t, w)["repeat"] == "" {
		t.Fatalf("mismatched password = %d %s", w.Code, w.Body)
	}

	// Lowercase and without the dash still works.
	w = p.do("POST", "/api/auth/setup", setupBody(strings.ToLower(strings.ReplaceAll(code, "-", "")), "admin", pass, pass), nil)
	if w.Code != 200 {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	c := cookieOf(t, w)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v must be HttpOnly, Secure, SameSite=Strict", c)
	}
	if w := p.do("GET", "/api/auth/session", "", c); w.Code != 200 || !strings.Contains(w.Body.String(), `"username":"admin"`) {
		t.Fatalf("session after setup = %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(p.cfg.AdminDir, "setup-code")); !os.IsNotExist(err) {
		t.Fatalf("setup-code file should be gone once the admin exists: %v", err)
	}

	w = p.do("POST", "/api/auth/setup", setupBody(code, "other", pass, pass), nil)
	if w.Code == 200 {
		t.Fatalf("code reused: %d %s", w.Code, w.Body)
	}
}

func TestSetupCodeSurvivesRestartButNotExpiry(t *testing.T) {
	p := newPanel(t)
	code := p.setupCode()
	p.app.Close()
	p.open()
	if p.setupCode() != code {
		t.Fatal("a restart within 24 hours must keep the printed code")
	}
	p.now = p.now.Add(25 * time.Hour)
	w := p.do("POST", "/api/auth/setup", setupBody(code, "admin", pass, pass), nil)
	if w.Code != 400 || !strings.Contains(fieldErrors(t, w)["code"], "expired") {
		t.Fatalf("expired code = %d %s", w.Code, w.Body)
	}
	p.app.Close()
	p.open()
	if p.setupCode() == code {
		t.Fatal("the next start after expiry must make a new code")
	}
}

func TestLoginLockout(t *testing.T) {
	p := newPanel(t)
	p.signUp()

	for i := 1; i <= 4; i++ {
		w := p.login("wrong password!")
		if w.Code != 401 {
			t.Fatalf("failure %d = %d %s", i, w.Code, w.Body)
		}
		msg := fieldErrors(t, w)[""]
		if wantLeft := i >= 3; strings.Contains(msg, "left") != wantLeft {
			t.Fatalf("failure %d message %q: tries-left shown from the 3rd failure only", i, msg)
		}
	}
	if w := p.login("wrong password!"); w.Code != 429 {
		t.Fatalf("5th failure = %d %s, want 429", w.Code, w.Body)
	}
	if w := p.login(pass); w.Code != 429 {
		t.Fatalf("right password while locked = %d, want 429", w.Code)
	}
	p.now = p.now.Add(4 * time.Minute)
	if w := p.login(pass); w.Code != 429 {
		t.Fatalf("after 4 minutes = %d, want 429", w.Code)
	}
	p.now = p.now.Add(time.Minute)
	if w := p.login(pass); w.Code != 200 {
		t.Fatalf("after 5 minutes = %d %s", w.Code, w.Body)
	}
}

func TestSessionExpiresWhenIdle(t *testing.T) {
	p := newPanel(t)
	c := p.signUp()

	p.now = p.now.Add(7 * time.Hour)
	if w := p.do("GET", "/api/auth/session", "", c); w.Code != 200 {
		t.Fatalf("after 7 h = %d", w.Code)
	}
	p.now = p.now.Add(7 * time.Hour) // 14 h old, but used 7 h ago
	if w := p.do("GET", "/api/auth/session", "", c); w.Code != 200 {
		t.Fatalf("use must keep the session alive: %d", w.Code)
	}
	p.now = p.now.Add(8 * time.Hour)
	if w := p.do("GET", "/api/auth/session", "", c); w.Code != 401 {
		t.Fatalf("after 8 h idle = %d, want 401", w.Code)
	}
}

func TestChangePasswordEndsOtherSessions(t *testing.T) {
	p := newPanel(t)
	mine := p.signUp()
	other := cookieOf(t, p.login(pass))

	if w := p.do("GET", "/api/account/sessions", "", mine); strings.Count(w.Body.String(), `"since"`) != 2 {
		t.Fatalf("session list = %s, want 2 sessions", w.Body)
	}
	body := func(cur, npw, rep string) string {
		b, _ := json.Marshal(map[string]string{"current": cur, "password": npw, "repeat": rep})
		return string(b)
	}
	const npw = "a brand new password"
	if w := p.do("POST", "/api/auth/password", body("not it at all", npw, npw), mine); w.Code != 400 || fieldErrors(t, w)["current"] == "" {
		t.Fatalf("wrong current password = %d %s", w.Code, w.Body)
	}
	if w := p.do("POST", "/api/auth/password", body(pass, npw, npw), mine); w.Code != 200 {
		t.Fatalf("change password = %d %s", w.Code, w.Body)
	}
	if w := p.do("GET", "/api/auth/session", "", other); w.Code != 401 {
		t.Fatalf("other session = %d, want 401", w.Code)
	}
	if w := p.do("GET", "/api/auth/session", "", mine); w.Code != 200 {
		t.Fatalf("own session = %d, want 200", w.Code)
	}
	if w := p.login(pass); w.Code != 401 {
		t.Fatalf("old password = %d, want 401", w.Code)
	}
	if w := p.login(npw); w.Code != 200 {
		t.Fatalf("new password = %d", w.Code)
	}
}

func TestStateChangesNeedSameOriginJSON(t *testing.T) {
	p := newPanel(t)
	p.signUp()
	send := func(origin, ctype string) int {
		req := httptest.NewRequest("POST", "https://panel.test/api/auth/login", strings.NewReader(loginBody("admin", pass)))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("Content-Type", ctype)
		w := httptest.NewRecorder()
		p.app.Handler().ServeHTTP(w, req)
		return w.Code
	}
	if c := send("https://evil.test", "application/json"); c != 403 {
		t.Fatalf("foreign Origin = %d, want 403", c)
	}
	if c := send("", "application/json"); c != 403 {
		t.Fatalf("no Origin = %d, want 403", c)
	}
	if c := send(origin, "text/plain"); c != 403 {
		t.Fatalf("form post = %d, want 403", c)
	}
	if c := send(origin, "application/json; charset=utf-8"); c != 200 {
		t.Fatalf("same-origin JSON = %d, want 200", c)
	}
}

func TestResetAdminIssuesWorkingCode(t *testing.T) {
	p := newPanel(t)
	c := p.signUp()
	p.app.Close()

	code, err := admin.ResetAdmin(p.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.setupCode() != code {
		t.Fatalf("setup-code file %q, printed %q", p.setupCode(), code)
	}
	p.open()
	if w := p.do("GET", "/api/auth/session", "", c); w.Code != 401 {
		t.Fatalf("old session after reset = %d, want 401", w.Code)
	}
	if w := p.login(pass); w.Code != 401 {
		t.Fatalf("old admin after reset = %d, want 401", w.Code)
	}
	w := p.do("POST", "/api/auth/setup", setupBody(code, "operator", pass+"!", pass+"!"), nil)
	if w.Code != 200 {
		t.Fatalf("setup with new code = %d %s", w.Code, w.Body)
	}
}

func TestResetAdminRefusesWhilePanelRuns(t *testing.T) {
	p := newPanel(t)
	if _, err := admin.ResetAdmin(p.cfg); err != admin.ErrBusy {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
}

// The sign-in page shows how to reset the admin from a shell on the host,
// for the way the panel runs: the panel must be stopped for it.
func TestTheSignInPageSaysHowToReset(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ container, want string }{
		{"", "systemctl stop thefeed-admin && " + exe + " reset-admin && systemctl start thefeed-admin"},
		{"1", "docker stop thefeed-admin && docker run --rm --volumes-from thefeed-admin $(docker inspect -f '{{.Config.Image}}' thefeed-admin) reset-admin && docker start thefeed-admin"},
	} {
		t.Setenv("THEFEED_ADMIN_CONTAINER", c.container)
		var got struct{ Reset string }
		if err := json.NewDecoder(newPanel(t).do("GET", "/api/auth/setup", "", nil).Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got.Reset != c.want {
			t.Errorf("container %q: reset = %q, want %q", c.container, got.Reset, c.want)
		}
	}
}

func TestModuleRoutesBucketsAndActionRecord(t *testing.T) {
	demo := func(d *admin.Deps) error {
		b, err := d.Bucket("demo.things")
		if err != nil {
			return err
		}
		d.Handle("POST /api/demo/restart", func(w http.ResponseWriter, r *http.Request) {
			var n int
			b.Get("count", &n)
			b.Put("count", n+1)
			d.Record(admin.Action{Action: "Restart", Outcome: "Back up"})
			admin.WriteJSON(w, 200, map[string]int{"count": n + 1})
		})
		return nil
	}
	p := newPanel(t, demo)
	if w := p.do("POST", "/api/demo/restart", "{}", nil); w.Code != 401 {
		t.Fatalf("module route without session = %d, want 401", w.Code)
	}
	c := p.signUp()
	p.do("POST", "/api/demo/restart", "{}", c)
	p.now = p.now.Add(time.Minute)
	if w := p.do("POST", "/api/demo/restart", "{}", c); !strings.Contains(w.Body.String(), `"count":2`) {
		t.Fatalf("module bucket didn't keep its value: %s", w.Body)
	}
	if w := p.do("GET", "/api/actions", "", nil); w.Code != 401 {
		t.Fatalf("action record without session = %d, want 401", w.Code)
	}
	var out struct{ Actions []admin.Action }
	json.Unmarshal(p.do("GET", "/api/actions?limit=1", "", c).Body.Bytes(), &out)
	if len(out.Actions) != 1 || out.Actions[0].Action != "Restart" || !out.Actions[0].At.Equal(p.now) {
		t.Fatalf("newest action = %+v, want the second Restart at %v", out.Actions, p.now)
	}
}
