package alerts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/alerts"
)

const (
	origin = "https://panel.test"
	pass   = "correct horse battery"
	// tgURL is a Telegram URL with a bot token in it, as Shoutrrr writes them.
	tgURL = "telegram://123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw@telegram?chats=@ops"
)

// sender is a fake Shoutrrr: it keeps what it sent, and fails while down,
// with the URL in its error the way Telegram's does.
type sender struct {
	sent []string
	down bool
}

func (s *sender) Check(u string) error {
	if !strings.Contains(u, "://") {
		return errors.New("unknown service")
	}
	return nil
}

func (s *sender) Send(u, msg string) error {
	if s.down {
		return errors.New(`Post "https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/sendMessage": dial tcp: i/o timeout`)
	}
	s.sent = append(s.sent, msg)
	return nil
}

// panel is the real admin app with the alerts module, signed in, a clock
// the test moves, and the module's minute tick run by hand.
type panel struct {
	t      *testing.T
	cfg    admin.Config
	now    time.Time
	app    *admin.App
	deps   *admin.Deps
	s      *sender
	tick   func()
	cookie *http.Cookie
}

func newPanel(t *testing.T) *panel {
	t.Helper()
	p := &panel{t: t, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC), s: &sender{}}
	data := t.TempDir()
	os.WriteFile(filepath.Join(data, "thefeed.env"), []byte("THEFEED_DOMAIN=v.example.com\n"), 0o600)
	p.cfg = admin.Config{AdminDir: t.TempDir(), ServerDataDir: data, Clock: admin.ClockFunc(func() time.Time { return p.now })}
	p.open()
	code, err := os.ReadFile(filepath.Join(p.cfg.AdminDir, "setup-code"))
	if err != nil {
		t.Fatal(err)
	}
	w := p.do("POST", "/api/auth/setup", map[string]string{"code": strings.TrimSpace(string(code)), "username": "admin", "password": pass, "repeat": pass})
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

// open starts the panel on p.cfg; again after Close, it's a restart.
func (p *panel) open() {
	p.t.Helper()
	grab := func(d *admin.Deps) error { p.deps = d; return nil }
	app, err := admin.New(p.cfg, grab, alerts.New(alerts.Options{Sender: p.s, Every: func(_ time.Duration, f func()) { p.tick = f }}))
	if err != nil {
		p.t.Fatal(err)
	}
	p.app = app
	p.t.Cleanup(func() { app.Close() })
}

func (p *panel) do(method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, origin+path, bytes.NewReader(b))
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

// at moves the clock to minute m, publishes readings then, and ticks.
func (p *panel) at(m int, rs map[string]admin.Reading) {
	p.now = time.Date(2026, 11, 6, 12, m, 0, 0, time.UTC)
	for k, r := range rs {
		p.deps.Publish(k, r)
	}
	p.tick()
}

func (p *panel) addURL() {
	p.t.Helper()
	if w := p.do("POST", "/api/alerts/urls", map[string]string{"url": tgURL, "password": pass}); w.Code != 200 {
		p.t.Fatalf("add URL = %d %s", w.Code, w.Body)
	}
}

func (p *panel) wantSent(want ...string) {
	p.t.Helper()
	if strings.Join(p.s.sent, "\n") != strings.Join(want, "\n") {
		p.t.Fatalf("sent:\n%s\nwant:\n%s", strings.Join(p.s.sent, "\n"), strings.Join(want, "\n"))
	}
}

func cpu(v float64) map[string]admin.Reading {
	return map[string]admin.Reading{"cpu": {Value: v, Name: "CPU"}}
}

// A rule sends once its condition has held for its minutes, not before,
// not again, and once more when it clears.
func TestAnAlertSendsOnceAndAgainWhenItClears(t *testing.T) {
	p := newPanel(t)
	p.addURL()
	for m := 0; m <= 9; m++ {
		p.at(m, cpu(97))
	}
	p.wantSent()
	p.at(10, cpu(97))
	p.at(11, cpu(98))
	p.wantSent("CPU on v.example.com is 97%, above 90% for 10 minutes.")
	p.at(12, cpu(20))
	p.wantSent("CPU on v.example.com is 97%, above 90% for 10 minutes.", "CPU on v.example.com is back to 20%.")
}

// A dip below the threshold starts the minutes over.
func TestADipStartsTheMinutesOver(t *testing.T) {
	p := newPanel(t)
	p.addURL()
	for m := 0; m <= 8; m++ {
		p.at(m, cpu(97))
	}
	p.at(9, cpu(50))
	for m := 10; m <= 18; m++ {
		p.at(m, cpu(97))
	}
	p.wantSent()
}

// While the server is stopped every feature reads down; only the server's
// alert goes out, and the features' follow if they're still down after.
func TestAStoppedServerSendsOneAlertNotOnePerFeature(t *testing.T) {
	p := newPanel(t)
	p.addURL()
	down := map[string]admin.Reading{"server": {State: "stopped"}, "feature.feed": {State: "down", Name: "DNS feed"}, "feature.x": {State: "down", Name: "X (Twitter)"}}
	for m := 0; m <= 10; m++ {
		p.at(m, down)
	}
	p.wantSent("thefeed-server on v.example.com is stopped, since 12:00 UTC.")
	back := map[string]admin.Reading{"server": {State: "running"}, "feature.feed": {State: "ok", Name: "DNS feed"}, "feature.x": {State: "ok", Name: "X (Twitter)"}}
	p.at(11, back)
	p.wantSent("thefeed-server on v.example.com is stopped, since 12:00 UTC.", "thefeed-server on v.example.com is running again, after 11 minutes.")
}

// Nothing counts as sent until a URL took it: a failed send is tried again
// the next minute.
func TestAFailedSendIsTriedAgain(t *testing.T) {
	p := newPanel(t)
	p.addURL()
	p.s.down = true
	for m := 0; m <= 10; m++ {
		p.at(m, cpu(97))
	}
	p.s.down = false
	p.wantSent()
	p.at(11, cpu(97))
	p.wantSent("CPU on v.example.com is 97%, above 90% for 10 minutes.")
}

// A restart of the panel doesn't send a firing alert twice; it still
// sends when the alert clears.
func TestAPanelRestartDoesntSendTwice(t *testing.T) {
	p := newPanel(t)
	p.addURL()
	for m := 0; m <= 10; m++ {
		p.at(m, cpu(97))
	}
	p.app.Close()
	p.open()
	for m := 11; m <= 25; m++ {
		p.at(m, cpu(97))
	}
	p.at(26, cpu(10))
	p.wantSent("CPU on v.example.com is 97%, above 90% for 10 minutes.", "CPU on v.example.com is back to 10%.")
}

// A reading that stopped coming neither fires nor clears.
func TestAStaleReadingDoesNothing(t *testing.T) {
	p := newPanel(t)
	p.addURL()
	p.at(0, cpu(97))
	for m := 1; m <= 20; m++ {
		p.at(m, nil)
	}
	p.wantSent()
}

// A URL holds a token: adding one takes the password, the API only ever
// shows its scheme and host, and errors from sending are scrubbed of it.
func TestURLsNeedThePasswordAndNeverShowTheirToken(t *testing.T) {
	p := newPanel(t)
	if w := p.do("POST", "/api/alerts/urls", map[string]string{"url": tgURL}); w.Code != http.StatusForbidden {
		t.Fatalf("add without the password = %d %s, want 403", w.Code, w.Body)
	}
	if w := p.do("POST", "/api/alerts/urls", map[string]string{"url": "not a url", "password": pass}); w.Code != http.StatusBadRequest {
		t.Fatalf("add a bad URL = %d %s, want 400", w.Code, w.Body)
	}
	p.addURL()
	p.s.down = true
	for _, w := range []*httptest.ResponseRecorder{p.do("GET", "/api/alerts", nil), p.do("POST", "/api/alerts/test", struct{}{})} {
		if w.Code != 200 || strings.Contains(w.Body.String(), "AAHdqTcv") || strings.Contains(w.Body.String(), "123456789") {
			t.Fatalf("%d, with the token in it?\n%s", w.Code, w.Body)
		}
	}
	if w := p.do("POST", "/api/alerts/test", struct{}{}); !strings.Contains(w.Body.String(), `"url":"telegram://telegram/…"`) || !strings.Contains(w.Body.String(), "i/o timeout") {
		t.Fatalf("test = %s, want the masked URL and why it failed", w.Body)
	}
}

func TestRulesAreSavedWithinLimits(t *testing.T) {
	p := newPanel(t)
	for _, bad := range []map[string]any{{"on": true, "above": 150, "minutes": 5}, {"on": true, "above": 80, "minutes": 0}} {
		if w := p.do("POST", "/api/alerts/rules/cpu", bad); w.Code != http.StatusBadRequest {
			t.Errorf("rule %v = %d, want 400", bad, w.Code)
		}
	}
	if w := p.do("POST", "/api/alerts/rules/cpu", map[string]any{"on": true, "above": 50, "minutes": 2}); w.Code != 200 {
		t.Fatalf("rule = %d %s", w.Code, w.Body)
	}
	p.addURL()
	p.at(0, cpu(60))
	p.at(2, cpu(60))
	p.wantSent("CPU on v.example.com is 60%, above 50% for 2 minutes.")
}
