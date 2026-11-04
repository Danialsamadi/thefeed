package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/health"
	"github.com/sartoopjj/thefeed/internal/controlapi"
)

// fakeServer is thefeed-server's control socket: the status the test set
// last, and the checks it was asked for.
type fakeServer struct {
	mu        sync.Mutex
	st        *controlapi.Status
	err       error
	checks    map[string]*controlapi.CheckResult
	asked     []string
	refreshed int
}

func (f *fakeServer) Status(context.Context) (*controlapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	// A copy through JSON, as the socket would give it.
	raw, _ := json.Marshal(f.st)
	var st controlapi.Status
	json.Unmarshal(raw, &st)
	return &st, nil
}

func (f *fakeServer) ReloadChannels(context.Context) error     { return nil }
func (f *fakeServer) SetAnswering(context.Context, bool) error { return nil }

func (f *fakeServer) Refresh(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed++
	return f.err
}

func (f *fakeServer) Check(_ context.Context, feature string) (*controlapi.CheckResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, feature)
	if res := f.checks[feature]; res != nil {
		return res, nil
	}
	return nil, errors.New("POST /checks/" + feature + ": 404 Not Found")
}

// fakeDNS is the feed self-test: every domain answers in 12 ms unless the
// test says otherwise.
type fakeDNS struct {
	mu   sync.Mutex
	took map[string]time.Duration
	err  map[string]error
}

func (f *fakeDNS) Query(_ context.Context, listen, domain string) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.err[domain]; err != nil {
		return 0, err
	}
	if d, ok := f.took[domain]; ok {
		return d, nil
	}
	return 12 * time.Millisecond, nil
}

type panel struct {
	t      *testing.T
	app    *admin.App
	cookie *http.Cookie
	srv    *fakeServer
	dns    *fakeDNS

	mu  sync.Mutex // the clock is read by the module's own loop too
	now time.Time
}

const (
	origin = "https://panel.test"
	pass   = "correct horse battery"
)

// newPanel is the real admin app with the health module on fakes, signed
// in, its clock at start and the server reporting st.
func newPanel(t *testing.T, start time.Time, st *controlapi.Status) *panel {
	t.Helper()
	t.Parallel() // each panel has its own directory and fakes
	p := &panel{t: t, now: start, srv: &fakeServer{st: st, checks: map[string]*controlapi.CheckResult{}},
		dns: &fakeDNS{took: map[string]time.Duration{}, err: map[string]error{}}}
	cfg := admin.Config{AdminDir: t.TempDir(), Clock: admin.ClockFunc(p.clock)}
	app, err := admin.New(cfg, health.New(p.srv, p.dns))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	p.app = app
	code, err := os.ReadFile(filepath.Join(cfg.AdminDir, "setup-code"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(string(code)), "username": "admin", "password": pass, "repeat": pass})
	p.signIn("/api/auth/setup", string(body))
	return p
}

func (p *panel) clock() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.now
}

func (p *panel) signIn(path, body string) {
	p.t.Helper()
	w := p.send("POST", path, body)
	for _, c := range w.Result().Cookies() {
		if c.Name == "thefeed_admin" && c.Value != "" {
			p.cookie = c
			return
		}
	}
	p.t.Fatalf("POST %s = %d %s", path, w.Code, w.Body)
}

func (p *panel) send(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, origin+path, strings.NewReader(body))
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

// do sends a request, signing in again when the session has run out, as
// the operator would after days away.
func (p *panel) do(method, path, body string) *httptest.ResponseRecorder {
	p.t.Helper()
	w := p.send(method, path, body)
	if w.Code == http.StatusUnauthorized {
		p.signIn("/api/auth/login", `{"username":"admin","password":"`+pass+`"}`)
		w = p.send(method, path, body)
	}
	return w
}

func (p *panel) get(path string, out any) {
	p.t.Helper()
	w := p.do("GET", path, "")
	if w.Code != 200 {
		p.t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		p.t.Fatalf("GET %s: %v in %s", path, err, w.Body)
	}
}

func (p *panel) board() board {
	p.t.Helper()
	var b board
	p.get("/api/board", &b)
	return b
}

// at moves the clock to t, with the server reporting st (nil: an error
// from the control socket, as when the server is stopped).
func (p *panel) at(t time.Time, st *controlapi.Status) {
	p.srv.mu.Lock()
	p.srv.st, p.srv.err = st, nil
	if st == nil {
		p.srv.err = errors.New("dial unix /opt/thefeed/data/control.sock: connect: no such file or directory")
	}
	p.srv.mu.Unlock()
	p.mu.Lock()
	p.now = t
	p.mu.Unlock()
}

// minute moves the clock a minute on, with the server reporting the
// healthy status changed by change, and reads the Board.
func (p *panel) minute(change func(*controlapi.Status)) board {
	p.t.Helper()
	next := p.clock().Add(time.Minute)
	st := healthy(next)
	if change != nil {
		change(st)
	}
	p.at(next, st)
	return p.board()
}

type board struct {
	Server struct {
		State, Version  string
		Since, LastSeen time.Time
	}
	Attention int
	Features  []row
}

type row struct {
	ID, Name, State, Word, Line string
	Uptime                      uptime
}

type uptime struct {
	Days                []string
	Percent             *float64
	Up, Slow, Down, Off int
}

func (b board) row(id string) row {
	for _, r := range b.Features {
		if r.ID == id {
			return r
		}
	}
	return row{}
}

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func TestStoppedServerTakesEveryFeatureDown(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	p.minute(nil)

	p.at(t0.Add(2*time.Minute), nil)
	b := p.board()
	for _, r := range b.Features {
		if r.State != "down" || r.Word != "Stopped" || r.Line != "With the server since 12:02" {
			t.Errorf("%s = %s %s %q, want down Stopped \"With the server since 12:02\"", r.ID, r.State, r.Word, r.Line)
		}
	}
	if b.Attention != 7 || b.Server.State != "stopped" || !b.Server.LastSeen.Equal(t0.Add(time.Minute)) || !b.Server.Since.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("attention %d, server %+v; want 7, stopped since 12:02, last seen 12:01", b.Attention, b.Server)
	}

	b = p.minute(func(st *controlapi.Status) { st.Server.Answering = false })
	b = p.minute(func(st *controlapi.Status) { st.Server.Answering = false })
	for _, r := range b.Features {
		if r.State != "down" || r.Word != "Not answering" || r.Line != "With the server since 12:03" {
			t.Errorf("%s = %s %s %q, want down \"Not answering\" \"With the server since 12:03\"", r.ID, r.State, r.Word, r.Line)
		}
	}
	if b.Server.State != "not answering" || !b.Server.LastSeen.Equal(t0.Add(4*time.Minute)) {
		t.Errorf("server %+v, want not answering, last seen 12:04", b.Server)
	}
}

// healthy is a server with every feature on and working, as it would
// report at now.
func healthy(now time.Time) *controlapi.Status {
	var dcs []controlapi.DC
	for i, ms := range []int64{88, 140, 95, 160, 120} {
		dcs = append(dcs, controlapi.DC{ID: i + 1, OK: true, DialMs: ms, At: now.Add(-2 * time.Minute)})
	}
	return &controlapi.Status{
		Server: controlapi.Server{Version: "v0.41.0", Started: now.Add(-26 * time.Hour), Answering: true, Config: map[string]string{
			"server.listen": ":53",
			"dns.domain":    "t.example.com", "dns.extraDomains": "t2.example.com,t3.example.com",
			"chat.domains": "c.example.com", "chat.on": "true",
			"mt.domains": "m.example.com", "mt.on": "true",
			"media.dnsOn": "true", "media.dnsMaxKB": "200", "media.dnsTTLMin": "60", "media.dnsComp": "gzip",
			"media.ghOn": "true", "media.ghRepo": "owner/relay-repo", "media.ghBranch": "main",
			"chat.sendPerHour": "30", "chat.inboxCap": "50", "chat.perPairCap": "10", "chat.maxMsgBytes": "500", "chat.ttlHours": "72",
			"xacc.instances": "https://nitter.net,https://xcancel.com,https://nitter.poast.org",
			"fetch.every":    "10",
		}},
		Feed: controlapi.Feed{Domains: []controlapi.DomainQueries{
			{Domain: "t.example.com", Queries: 900000, PerMinute: 512},
			{Domain: "t2.example.com", Queries: 300000, PerMinute: 214},
			{Domain: "t3.example.com", Queries: 40512, PerMinute: 116},
			{Domain: "c.example.com", Queries: 5000, PerMinute: 30},
			{Domain: "m.example.com", Queries: 80000, PerMinute: 400},
		}},
		Channels: &controlapi.Channels{IntervalMs: 600000, Cycles: []controlapi.FetchCycle{
			{Start: now.Add(-16 * time.Minute), DurationMs: 178000, Fetched: 42, Total: 42, Posts: 9},
			{Start: now.Add(-6 * time.Minute), DurationMs: 192000, Fetched: 42, Total: 42, Posts: 318},
		}},
		Chat:       &controlapi.Chat{ActiveToday: 128, MessagesToday: 2304, Waiting: 41},
		MediaCache: &controlapi.MediaCacheStats{Entries: 3100, Bytes: 50 << 20, Queries: 18000, MaxFileBytes: 200 << 10, TTLSeconds: 3600},
		GHRelay:    &controlapi.RelayStatus{Repo: "owner/relay-repo", RepoSizeKB: 12 << 20, QuotaKB: 100 << 20, PercentUsed: 12, PendingFiles: 3},
		MTProto:    &controlapi.MTProto{Sessions: 14, LiveTunnels: 12, MaxTunnels: 50, DCs: dcs},
		X: &controlapi.X{Accounts: 6, LastPost: now.Add(-10 * time.Minute), Instances: []controlapi.Instance{
			{Host: "https://nitter.net", OK: true, LatencyMs: 300, At: now.Add(-time.Minute)},
			{Host: "https://xcancel.com", OK: true, LatencyMs: 450, At: now.Add(-time.Minute)},
			{Host: "https://nitter.poast.org", OK: true, LatencyMs: 600, At: now.Add(-time.Minute)},
		}},
	}
}

func TestBoardShowsEveryFeatureWorking(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	b := p.minute(nil)

	want := []row{
		{ID: "x", Name: "X (Twitter)", Line: "3 of 3 Nitter instances answer"},
		{ID: "tg", Name: "Telegram over DNS", Line: "14 users connected, 12 tunnels open"},
		{ID: "feed", Name: "DNS feed", Line: "842 queries a minute on 3 domains"},
		{ID: "chan", Name: "Telegram channels", Line: "42 channels, last cycle done at 11:58"},
		{ID: "chat", Name: "Messenger", Line: "128 addresses active today, 2,304 messages"},
		{ID: "dnsm", Name: "DNS media relay", Line: "3,100 files cached, 18,000 block queries served"},
		{ID: "gh", Name: "GitHub media relay", Line: "3 uploads waiting, repository at 12% of its budget"},
	}
	if len(b.Features) != len(want) {
		t.Fatalf("features = %+v, want %d", b.Features, len(want))
	}
	for i, w := range want {
		got := b.Features[i]
		if got.ID != w.ID || got.Name != w.Name || got.State != "ok" || got.Word != "Working" || got.Line != w.Line {
			t.Errorf("feature %d = %s %q %s %q %q, want %s %q ok Working %q", i, got.ID, got.Name, got.State, got.Word, got.Line, w.ID, w.Name, w.Line)
		}
	}
	if b.Attention != 0 || b.Server.State != "running" || b.Server.Version != "v0.41.0" {
		t.Errorf("attention %d, server %+v; want 0, running v0.41.0", b.Attention, b.Server)
	}
}

// step is one sample: the healthy status changed by change, and the
// state and line the feature should show for it.
type step struct {
	change      func(*controlapi.Status)
	state, line string
}

// steps samples once a minute and checks feature id after each.
func steps(t *testing.T, id string, ss []step) *panel {
	t.Helper()
	p := newPanel(t, t0, healthy(t0))
	for i, s := range ss {
		r := p.minute(s.change).row(id)
		if r.State != s.state || r.Line != s.line || r.Word != words[s.state] {
			t.Errorf("step %d: %s = %s %s %q, want %s %s %q", i, id, r.State, r.Word, r.Line, s.state, words[s.state], s.line)
		}
	}
	return p
}

var words = map[string]string{"ok": "Working", "slow": "Slow", "down": "Down", "off": "Off"}

func TestXRules(t *testing.T) {
	steps(t, "x", []step{
		{nil, "ok", "3 of 3 Nitter instances answer"},
		{func(st *controlapi.Status) { st.X.Instances[0].OK, st.X.Instances[0].Error = false, "429 rate limited" }, "slow", "2 of 3 Nitter instances answer"},
		{func(st *controlapi.Status) {
			for i := range st.X.Instances {
				st.X.Instances[i].OK, st.X.Instances[i].Error = false, "timed out"
			}
		}, "down", "0 of 3 Nitter instances answer"},
		// A fresh start: nothing tried yet is not a failure.
		{func(st *controlapi.Status) {
			for i := range st.X.Instances {
				st.X.Instances[i] = controlapi.Instance{Host: st.X.Instances[i].Host}
			}
		}, "ok", "Nitter instances not checked yet"},
		{func(st *controlapi.Status) { st.X.Accounts = 0 }, "off", "No X accounts"},
		{func(st *controlapi.Status) { st.X = nil }, "off", "No X accounts"},
	})
}

func TestTelegramOverDNSRules(t *testing.T) {
	// failAll makes every data centre's last dial fail, ago before now.
	failAll := func(ago time.Duration) func(*controlapi.Status) {
		return func(st *controlapi.Status) {
			for i := range st.MTProto.DCs {
				dc := &st.MTProto.DCs[i]
				dc.OK, dc.Error, dc.At, dc.FailuresLastHour = false, "i/o timeout", dc.At.Add(2*time.Minute-ago), 1
			}
		}
	}
	steps(t, "tg", []step{
		{nil, "ok", "14 users connected, 12 tunnels open"},
		{func(st *controlapi.Status) { st.MTProto.DCs[1].FailuresLastHour = 2 }, "slow", "2 dial failures to data centre 2 in the last hour"},
		{func(st *controlapi.Status) {
			st.MTProto.DCs[1].FailuresLastHour, st.MTProto.DCs[3].FailuresLastHour = 1, 2
		}, "slow", "3 dial failures in the last hour"},
		{func(st *controlapi.Status) { st.MTProto.Sessions = 44 }, "ok", "44 users connected, 12 tunnels open"},
		{func(st *controlapi.Status) { st.MTProto.Sessions = 45 }, "slow", "45 of 50 users connected, near the cap"},
		{func(st *controlapi.Status) { st.MTProto.Sessions, st.MTProto.MaxTunnels = 450, 0 }, "ok", "450 users connected, 12 tunnels open"},
		{failAll(9 * time.Minute), "down", "Every data centre dial failed in the last 10 minutes"},
		// Failed, but not in the last 10 minutes: the failures still count for the hour.
		{failAll(11 * time.Minute), "slow", "5 dial failures in the last hour"},
		{func(st *controlapi.Status) { st.MTProto = nil }, "off", "Turned off in Control"},
	})
}

func TestDNSFeedRules(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	timeout := errors.New("read udp 127.0.0.1:53: i/o timeout")
	for i, s := range []struct {
		took        map[string]time.Duration
		err         map[string]error
		state, line string
	}{
		{nil, nil, "ok", "842 queries a minute on 3 domains"},
		{map[string]time.Duration{"t2.example.com": time.Second}, nil, "ok", "842 queries a minute on 3 domains"},
		{map[string]time.Duration{"t2.example.com": 1400 * time.Millisecond}, nil, "slow", "The test query took 1.4 s on t2.example.com"},
		{nil, map[string]error{"t2.example.com": timeout}, "down", "No answer to the test query on t2.example.com"},
		{nil, map[string]error{"t2.example.com": timeout, "t3.example.com": timeout}, "down", "No answer to the test query on t2.example.com, t3.example.com"},
	} {
		p.dns.mu.Lock()
		p.dns.took, p.dns.err = s.took, s.err
		p.dns.mu.Unlock()
		if r := p.minute(nil).row("feed"); r.State != s.state || r.Line != s.line {
			t.Errorf("step %d: feed = %s %q, want %s %q", i, r.State, r.Line, s.state, s.line)
		}
	}
}

func TestTelegramChannelsRules(t *testing.T) {
	// last changes the newest cycle; healthy's started 6 minutes ago and took 3 min 12 s.
	last := func(f func(c *controlapi.FetchCycle)) func(*controlapi.Status) {
		return func(st *controlapi.Status) { f(&st.Channels.Cycles[1]) }
	}
	// ago moves both cycles' starts back, keeping them 10 minutes apart.
	ago := func(d time.Duration) func(*controlapi.Status) {
		return func(st *controlapi.Status) {
			now := st.Channels.Cycles[1].Start.Add(6 * time.Minute)
			st.Channels.Cycles[0].Start, st.Channels.Cycles[1].Start = now.Add(-d-10*time.Minute), now.Add(-d)
		}
	}
	steps(t, "chan", []step{
		{nil, "ok", "42 channels, last cycle done at 11:58"},
		// 12:02. 80% of the 10-minute interval is 8 minutes.
		{last(func(c *controlapi.FetchCycle) { c.Start, c.DurationMs = c.Start.Add(-4*time.Minute), 480000 }), "ok", "42 channels, last cycle done at 12:00"},
		{last(func(c *controlapi.FetchCycle) { c.Start, c.DurationMs = c.Start.Add(-4*time.Minute), 481000 }), "slow", "The last cycle took 8 min 1 s of the 10 min interval"},
		// 12:04. 90% of 42 channels is 37.8.
		{last(func(c *controlapi.FetchCycle) { c.Fetched = 38 }), "ok", "42 channels, last cycle done at 12:01"},
		{last(func(c *controlapi.FetchCycle) { c.Fetched = 37 }), "slow", "The last cycle fetched 37 of 42 channels"},
		{last(func(c *controlapi.FetchCycle) { c.Fetched = 0 }), "down", "The last cycle fetched none of the 42 channels"},
		// 12:07. Three intervals are 30 minutes.
		{ago(30 * time.Minute), "ok", "42 channels, last cycle done at 11:40"},
		{ago(31 * time.Minute), "down", "No channels fetched in the last 31 min"},
		{func(st *controlapi.Status) {
			st.Channels.Cycles, st.Server.Started = nil, st.Server.Started.Add(26*time.Hour-20*time.Minute)
		}, "ok", "Waiting for the first fetch cycle"},
		{func(st *controlapi.Status) {
			st.Channels.Cycles, st.Server.Started = nil, st.Server.Started.Add(26*time.Hour-31*time.Minute)
		}, "down", "No channels fetched in the last 31 min"},
		{last(func(c *controlapi.FetchCycle) { c.Fetched, c.Total = 1, 1 }), "ok", "1 channel, last cycle done at 12:08"},
		{func(st *controlapi.Status) { st.Channels = nil }, "off", "Turned off in Control"},
	})
}

func TestMessengerRules(t *testing.T) {
	steps(t, "chat", []step{
		{nil, "ok", "128 addresses active today, 2,304 messages"},
		{func(st *controlapi.Status) { st.Chat.StoreErrors = 1 }, "down", "1 message store error in the last 10 minutes"},
		{func(st *controlapi.Status) { st.Chat.StoreErrors = 5 }, "down", "5 message store errors in the last 10 minutes"},
		{func(st *controlapi.Status) { st.Chat = nil }, "off", "Turned off in Control"},
	})
}

func TestDNSMediaRules(t *testing.T) {
	steps(t, "dnsm", []step{
		{nil, "ok", "3,100 files cached, 18,000 block queries served"},
		{func(st *controlapi.Status) { st.MediaCache.Errors = 1 }, "down", "1 file the media cache couldn't store in the last 10 minutes"},
		{func(st *controlapi.Status) { st.MediaCache.Errors = 2 }, "down", "2 files the media cache couldn't store in the last 10 minutes"},
		{func(st *controlapi.Status) { st.MediaCache = nil }, "down", "The media cache isn't running"},
		// The cache stays up for the GitHub relay alone.
		{func(st *controlapi.Status) { st.Server.Config["media.dnsOn"] = "false" }, "off", "Turned off in Control"},
	})
}

func TestGitHubRelayRules(t *testing.T) {
	gh := func(f func(g *controlapi.RelayStatus)) func(*controlapi.Status) {
		return func(st *controlapi.Status) { f(st.GHRelay) }
	}
	steps(t, "gh", []step{
		{nil, "ok", "3 uploads waiting, repository at 12% of its budget"},
		{gh(func(g *controlapi.RelayStatus) { g.PendingFiles = 50 }), "ok", "50 uploads waiting, repository at 12% of its budget"},
		{gh(func(g *controlapi.RelayStatus) { g.PendingFiles = 51 }), "slow", "51 uploads waiting"},
		{gh(func(g *controlapi.RelayStatus) { g.PercentUsed = 90 }), "ok", "3 uploads waiting, repository at 90% of its budget"},
		{gh(func(g *controlapi.RelayStatus) { g.PercentUsed = 93.2 }), "slow", "The repository is at 93% of its budget"},
		{gh(func(g *controlapi.RelayStatus) { g.FailStreak = 2 }), "ok", "3 uploads waiting, repository at 12% of its budget"},
		{gh(func(g *controlapi.RelayStatus) { g.FailStreak, g.LastError = 3, "upload: 502 Bad Gateway" }), "down", "3 uploads failed in a row: upload: 502 Bad Gateway"},
		{gh(func(g *controlapi.RelayStatus) { g.TokenDenied = true }), "down", "GitHub refuses the token"},
		{gh(func(g *controlapi.RelayStatus) { g.Quota403 = true }), "down", "The GitHub quota is used up"},
		{func(st *controlapi.Status) { st.GHRelay = nil }, "off", "Turned off in Control"},
	})
}

// xDown has every Nitter instance failing; xSlow has one.
func xDown(st *controlapi.Status) {
	for i := range st.X.Instances {
		st.X.Instances[i].OK, st.X.Instances[i].Error = false, "timed out"
	}
}

func xSlow(st *controlapi.Status) {
	st.X.Instances[0].OK, st.X.Instances[0].Error = false, "429 rate limited"
}

// The mockup's X: 60 days, slow on days 30, 31 and 34, down from day 37 to
// today. 37 days weren't down of 60 that weren't off: 61.7%.
func TestUptimeIsDaysNotDownOfDaysNotOff(t *testing.T) {
	first := t0.AddDate(0, 0, -59).Truncate(24 * time.Hour)
	p := newPanel(t, first, healthy(first))
	sampleAt := func(at time.Time, change func(*controlapi.Status)) board {
		st := healthy(at)
		if change != nil {
			change(st)
		}
		p.at(at, st)
		return p.board()
	}
	var b board
	for day := range 60 {
		at := first.AddDate(0, 0, day)
		switch {
		case day >= 37:
			// 30 minutes down make a down day.
			for m := range 30 {
				b = sampleAt(at.Add(time.Duration(m)*time.Minute), xDown)
			}
		case day == 30 || day == 31 || day == 34:
			sampleAt(at, xSlow)
		default:
			sampleAt(at, nil)
		}
		if day == 59 {
			break
		}
		// Every 6 hours, so the session doesn't go idle.
		for hr := 6; hr < 24; hr += 6 {
			if day >= 37 {
				sampleAt(at.Add(time.Duration(hr)*time.Hour), xDown)
			} else {
				sampleAt(at.Add(time.Duration(hr)*time.Hour), nil)
			}
		}
	}

	u := b.row("x").Uptime
	if u.Up != 34 || u.Slow != 3 || u.Down != 23 || u.Off != 0 || u.Percent == nil || *u.Percent != 61.7 {
		t.Errorf("x uptime = %d up, %d slow, %d down, %d off, %v; want 34, 3, 23, 0, 61.7", u.Up, u.Slow, u.Down, u.Off, *u.Percent)
	}
	if u.Days[29] != "up" || u.Days[30] != "slow" || u.Days[36] != "up" || u.Days[37] != "down" || u.Days[59] != "down" {
		t.Errorf("x days = %v, want slow on 30, 31, 34 and down from 37", u.Days)
	}
	if g := b.row("gh").Uptime; g.Up != 60 || g.Percent == nil || *g.Percent != 100 {
		t.Errorf("gh uptime = %d up, %v; want 60, 100", g.Up, g.Percent)
	}
}

// Days end at midnight in the panel's time zone, not UTC's.
func TestDaysRollUpAtLocalMidnight(t *testing.T) {
	tehran := time.FixedZone("IRST", 3*3600+1800)
	start := time.Date(2026, 11, 3, 23, 29, 0, 0, tehran) // 19:59 UTC
	p := newPanel(t, start, healthy(start))
	p.board()
	// Stopped from 23:30 to 00:28: 30 minutes on the 3rd, 29 on the 4th.
	for m := 1; m <= 59; m++ {
		p.at(start.Add(time.Duration(m)*time.Minute), nil)
		p.board()
	}
	end := start.Add(time.Hour) // 00:29 on the 4th, 20:29 UTC on the 3rd
	p.at(end, healthy(end))
	b := p.board()

	off58 := make([]string, 58)
	for i := range off58 {
		off58[i] = "off"
	}
	for _, r := range b.Features {
		u := r.Uptime
		if len(u.Days) != 60 || strings.Join(u.Days[:58], " ") != strings.Join(off58, " ") || u.Days[58] != "down" || u.Days[59] != "slow" {
			t.Errorf("%s days = %v, want 58 off, then down (the 3rd), slow (today)", r.ID, u.Days)
		}
		if u.Up != 0 || u.Slow != 1 || u.Down != 1 || u.Off != 58 || u.Percent == nil || *u.Percent != 50 {
			t.Errorf("%s uptime = %d up, %d slow, %d down, %d off, %v%%; want 0, 1, 1, 58, 50%%", r.ID, u.Up, u.Slow, u.Down, u.Off, u.Percent)
		}
	}
}

type detail struct {
	ID, Name, State, Word, Line, Impact string
	Since                               time.Time
	Actions                             []struct{ ID, Label, Page string }
	Facts                               []struct{ Label, Value, State string }
	Uptime                              uptime
	Deps                                struct {
		Title string
		Cols  []string
		Rows  [][]string
	}
	Incidents            []incident
	LastCheck, NextCheck time.Time
	Checked              *checkResult
}

type incident struct {
	Start   time.Time
	End     *time.Time
	Worst   string
	Summary string
}

type checkResult struct {
	At              time.Time
	OK, Pending     bool
	Summary, Detail string
}

func (p *panel) feature(id string) detail {
	p.t.Helper()
	var d detail
	p.get("/api/features/"+id, &d)
	return d
}

// An incident opens on the second sample in a row that isn't ok, and
// closes on the second ok one.
func TestIncidentsOpenAndClose(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	for _, change := range []func(*controlapi.Status){
		nil,   // 12:01
		xDown, // 12:02, on its own: no incident
		nil,
		xSlow, // 12:04: the incident starts here
		xSlow, // 12:05: and opens, slow
		xDown, // 12:06: and gets worse
		nil,
		xDown, // 12:08: one ok sample doesn't close it
		nil,   // 12:09: it ends here
		nil,   // 12:10: and closes
		xDown, // 12:11
		xDown, // 12:12: a second one, still going
	} {
		p.minute(change)
	}
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	end := at(9)
	want := []incident{
		{Start: at(11), Worst: "down", Summary: "0 of 3 Nitter instances answer"},
		{Start: at(4), End: &end, Worst: "down", Summary: "0 of 3 Nitter instances answer"},
	}
	got := p.feature("x").Incidents
	if len(got) != len(want) {
		t.Fatalf("incidents = %+v, want %+v", got, want)
	}
	for i, w := range want {
		g := got[i]
		if !g.Start.Equal(w.Start) || (g.End == nil) != (w.End == nil) || (g.End != nil && !g.End.Equal(*w.End)) || g.Worst != w.Worst || g.Summary != w.Summary {
			t.Errorf("incident %d = %+v (end %v), want %+v (end %v)", i, g, g.End, w, w.End)
		}
	}
	if got := p.feature("gh").Incidents; got == nil || len(got) != 0 {
		t.Errorf("gh incidents = %#v, want an empty list", got)
	}
}

// A slow incident keeps the summary of what made it slow.
func TestIncidentKeepsTheWorstSummary(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	for _, change := range []func(*controlapi.Status){xSlow, xSlow, nil, nil} {
		p.minute(change)
	}
	end := t0.Add(3 * time.Minute)
	got := p.feature("x").Incidents
	if len(got) != 1 || !got[0].Start.Equal(t0.Add(time.Minute)) || got[0].End == nil || !got[0].End.Equal(end) || got[0].Worst != "slow" || got[0].Summary != "2 of 3 Nitter instances answer" {
		t.Errorf("incidents = %+v, want one slow from 12:01 to 12:03, \"2 of 3 Nitter instances answer\"", got)
	}
}

// js is v as JSON, for comparing shapes in one go.
func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestFeaturePageSaysWhatIsWrong(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	broken := func(st *controlapi.Status) {
		in := st.X.Instances
		in[0].OK, in[0].Error = false, "429 rate limited"
		in[1].OK, in[1].Error = false, "timed out after 10 s"
		in[2] = controlapi.Instance{Host: in[2].Host} // not tried yet
	}
	p.minute(nil)    // 12:01
	p.minute(broken) // 12:02
	p.minute(broken) // 12:03
	d := p.feature("x")

	if d.ID != "x" || d.Name != "X (Twitter)" || d.State != "down" || d.Word != "Down" || d.Line != "0 of 3 Nitter instances answer" || !d.Since.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("status = %s %q %s %s %q since %v; want x \"X (Twitter)\" down Down \"0 of 3 Nitter instances answer\" since 12:02", d.ID, d.Name, d.State, d.Word, d.Line, d.Since)
	}
	if d.Impact != "X accounts are paused. Telegram channels are not affected." {
		t.Errorf("impact = %q", d.Impact)
	}
	if got, want := js(d.Actions), `[{"ID":"check","Label":"Check now","Page":""},{"ID":"edit","Label":"Edit instances","Page":""}]`; got != want {
		t.Errorf("actions = %s\nwant      %s", got, want)
	}
	// Today has 1 ok and 2 down minutes: a slow day, which isn't down.
	if got, want := js(d.Facts), `[{"Label":"Instances answering","Value":"0 of 3","State":"down"},{"Label":"X accounts","Value":"6, paused","State":""},{"Label":"Last X post fetched","Value":"11:53","State":""},{"Label":"Uptime, 60 days","Value":"100%","State":""}]`; got != want {
		t.Errorf("facts = %s\nwant    %s", got, want)
	}
	if got, want := js(d.Deps), `{"Title":"Nitter instances","Cols":["Instance","Status","Result"],"Rows":[["https://nitter.net","down","429 rate limited"],["https://xcancel.com","down","timed out after 10 s"],["https://nitter.poast.org","pending","not checked yet"]]}`; got != want {
		t.Errorf("deps = %s\nwant   %s", got, want)
	}
	if u := d.Uptime; len(u.Days) != 60 || u.Days[59] != "slow" || u.Slow != 1 || u.Off != 59 || u.Percent == nil || *u.Percent != 100 {
		t.Errorf("uptime = %+v, want today slow, 59 days off, 100%%", u)
	}
	if len(d.Incidents) != 1 || !d.Incidents[0].Start.Equal(t0.Add(2*time.Minute)) || d.Incidents[0].End != nil {
		t.Errorf("incidents = %+v, want one from 12:02, going on", d.Incidents)
	}
	if !d.LastCheck.Equal(t0.Add(3*time.Minute)) || !d.NextCheck.Equal(t0.Add(4*time.Minute)) || d.Checked != nil {
		t.Errorf("checks = last %v, next %v, checked %+v; want 12:03, 12:04, none", d.LastCheck, d.NextCheck, d.Checked)
	}
}

// Each page's facts and dependencies, from healthy's numbers at 12:01.
func TestFeaturePagesShowTheirNumbers(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	p.minute(nil)
	for _, w := range []struct{ id, impact, actions, facts, deps string }{
		{"x", "X posts reach readers.",
			`[{"ID":"check","Label":"Check now","Page":""},{"ID":"edit","Label":"Edit instances","Page":""}]`,
			`[{"Label":"Instances answering","Value":"3 of 3","State":""},{"Label":"X accounts","Value":"6","State":""},{"Label":"Last X post fetched","Value":"11:51","State":""},{"Label":"Uptime, 60 days","Value":"100%","State":""}]`,
			`{"Title":"Nitter instances","Cols":["Instance","Status","Result"],"Rows":[["https://nitter.net","ok","answered in 300 ms"],["https://xcancel.com","ok","answered in 450 ms"],["https://nitter.poast.org","ok","answered in 600 ms"]]}`},
		{"tg", "People can use Telegram over DNS.",
			`[{"ID":"check","Label":"Check data centres","Page":""},{"ID":"page","Label":"Tunnel settings","Page":"mt"}]`,
			`[{"Label":"Tunnels open","Value":"12","State":""},{"Label":"Users connected","Value":"14 of 50","State":""},{"Label":"Dial failures, last hour","Value":"0","State":""},{"Label":"Slowest data centre","Value":"DC 4, 160 ms","State":""}]`,
			`{"Title":"Telegram data centres","Cols":["Data centre","Status","Dial time"],"Rows":[["DC 1","ok","88 ms"],["DC 2","ok","140 ms"],["DC 3","ok","95 ms"],["DC 4","ok","160 ms"],["DC 5","ok","120 ms"]]}`},
		{"feed", "Readers are getting the feed on all 3 domains.",
			`[{"ID":"check","Label":"Send a test query","Page":""}]`,
			`[{"Label":"Queries a minute","Value":"842","State":""},{"Label":"Queries since the server started","Value":"1,240,512","State":""},{"Label":"Domains answering","Value":"3 of 3","State":""},{"Label":"Slowest test query","Value":"12 ms","State":""}]`,
			`{"Title":"Domains","Cols":["Domain","Status","Queries a minute","Test query"],"Rows":[["t.example.com","ok","512","12 ms"],["t2.example.com","ok","214","12 ms"],["t3.example.com","ok","116","12 ms"]]}`},
		{"chan", "Readers get new posts within about 10 min.",
			`[{"ID":"check","Label":"Fetch now","Page":""}]`,
			`[{"Label":"Channels, last cycle","Value":"42 of 42","State":""},{"Label":"Posts, last cycle","Value":"318","State":""},{"Label":"Cycle took","Value":"3 min 12 s","State":""},{"Label":"Next cycle","Value":"12:05","State":""}]`,
			`{"Title":"Recent cycles","Cols":["Started","Channels","Posts","Took"],"Rows":[["11:55","42 of 42","318","3 min 12 s"],["11:45","42 of 42","9","2 min 58 s"]]}`},
		{"chat", "Messages are stored and delivered.",
			`[{"ID":"check","Label":"Send a test message","Page":""}]`,
			`[{"Label":"Active addresses today","Value":"128","State":""},{"Label":"Messages today","Value":"2,304","State":""},{"Label":"Waiting for pickup","Value":"41","State":""},{"Label":"Inboxes full today","Value":"0","State":""}]`,
			`{"Title":"Limits","Cols":["Limit","Value"],"Rows":[["Sends an hour, per address","30"],["Inbox size","50"],["Waiting per pair","10"],["Message size","500 bytes"],["Undelivered messages expire after","72 h"]]}`},
		{"dnsm", "Readers can load images and files up to 200 KB over DNS.",
			`[{"ID":"check","Label":"Check now","Page":""}]`,
			`[{"Label":"Files cached","Value":"3,100","State":""},{"Label":"Cache size","Value":"50 MB","State":""},{"Label":"Block queries served","Value":"18,000","State":""},{"Label":"Max file size","Value":"200 KB","State":""}]`,
			`{"Title":"Settings","Cols":["Setting","Value"],"Rows":[["Compression","gzip"],["Cache time","60 min"],["Max file size","200 KB"]]}`},
		{"gh", "Larger media reaches readers through GitHub.",
			`[{"ID":"check","Label":"Check GitHub","Page":""}]`,
			`[{"Label":"Uploads waiting","Value":"3","State":""},{"Label":"Repository size","Value":"12 GB of 100 GB","State":""},{"Label":"Token","Value":"works","State":""},{"Label":"Failures in a row","Value":"0","State":""}]`,
			`{"Title":"GitHub","Cols":["Check","Status","Detail"],"Rows":[["Repository","ok","owner/relay-repo, branch main, 12% of its budget"],["Token","ok","works"],["Quota","ok","not used up"],["Uploads","ok","3 waiting"]]}`},
	} {
		d := p.feature(w.id)
		if d.Impact != w.impact {
			t.Errorf("%s impact = %q, want %q", w.id, d.Impact, w.impact)
		}
		for _, c := range []struct{ what, got, want string }{
			{"actions", js(d.Actions), w.actions}, {"facts", js(d.Facts), w.facts}, {"deps", js(d.Deps), w.deps},
		} {
			if c.got != c.want {
				t.Errorf("%s %s = %s\nwant %s", w.id, c.what, c.got, c.want)
			}
		}
	}
}

// Off and Stopped pages say how to bring the feature back.
func TestOffAndStoppedPagesPointTheWayBack(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	p.minute(func(st *controlapi.Status) { st.Server.Config["media.dnsOn"], st.X.Accounts = "false", 0 })

	d := p.feature("dnsm")
	if d.State != "off" || d.Word != "Off" || d.Impact != "Turned off in Control. Readers' apps show it as turned off." ||
		js(d.Actions) != `[{"ID":"page","Label":"Open its settings","Page":"media"}]` || len(d.Facts) != 0 || d.Deps.Title != "" {
		t.Errorf("dnsm = %s %s %q %s facts %s deps %s", d.State, d.Word, d.Impact, js(d.Actions), js(d.Facts), js(d.Deps))
	}
	if d := p.feature("x"); d.State != "off" || d.Line != "No X accounts" || d.Impact != "Add X accounts in Control to read X." || js(d.Actions) != `[{"ID":"page","Label":"Open its settings","Page":"xacc"}]` {
		t.Errorf("x = %s %q %q %s", d.State, d.Line, d.Impact, js(d.Actions))
	}

	p.at(t0.Add(2*time.Minute), nil)
	// What the server last said stays, under Stopped.
	d = p.feature("feed")
	if d.State != "down" || d.Word != "Stopped" || d.Line != "With the server since 12:02" || !d.Since.Equal(t0.Add(2*time.Minute)) ||
		d.Impact != "Readers get nothing from it until the server answers again." || js(d.Actions) != `[{"ID":"page","Label":"Open Service and power","Page":"svc"}]` ||
		len(d.Facts) != 4 || d.Facts[0].Value != "842" {
		t.Errorf("feed = %s %s %q since %v %q %s facts %s", d.State, d.Word, d.Line, d.Since, d.Impact, js(d.Actions), js(d.Facts))
	}
}

func (p *panel) check(id string) (int, checkResult) {
	p.t.Helper()
	w := p.do("POST", "/api/checks/"+id, "{}")
	var res checkResult
	json.Unmarshal(w.Body.Bytes(), &res)
	return w.Code, res
}

// Check now runs the server's check, and samples again at once.
func TestCheckNowRunsTheServersCheck(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	p.minute(nil)
	p.srv.checks["x"] = &controlapi.CheckResult{OK: false, Summary: "None of the 3 instances answered.", Detail: "https://nitter.net 429 rate limited"}
	at := t0.Add(time.Minute + 20*time.Second) // the same minute
	st := healthy(at)
	xDown(st)
	p.at(at, st)

	code, res := p.check("x")
	if code != 200 || !res.At.Equal(at) || res.OK || res.Pending || res.Summary != "None of the 3 instances answered." || res.Detail != "https://nitter.net 429 rate limited" {
		t.Fatalf("check = %d %+v, want 200, not ok, the server's summary and detail, at 12:01:20", code, res)
	}
	d := p.feature("x")
	if d.State != "down" || !d.LastCheck.Equal(at) || d.Checked == nil || *d.Checked != res {
		t.Errorf("x after the check = %s, last check %v, checked %+v; want down, 12:01:20, the result", d.State, d.LastCheck, d.Checked)
	}
}

func TestChecksThatCantRun(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	p.minute(func(st *controlapi.Status) { st.GHRelay = nil })
	if code, _ := p.check("nope"); code != 404 {
		t.Errorf("unknown feature = %d, want 404", code)
	}
	if w := p.do("POST", "/api/checks/gh", "{}"); w.Code != 409 || !strings.Contains(w.Body.String(), "GitHub media relay is turned off.") {
		t.Errorf("a feature that's off = %d %s, want 409 \"GitHub media relay is turned off.\"", w.Code, w.Body)
	}
	p.at(t0.Add(2*time.Minute), nil)
	if w := p.do("POST", "/api/checks/tg", "{}"); w.Code != 502 || !strings.Contains(w.Body.String(), "404 Not Found") {
		t.Errorf("check the server can't run = %d %s, want 502 with the reason", w.Code, w.Body)
	}
	if strings.Join(p.srv.asked, " ") != "tg" {
		t.Errorf("the server was asked for %v, want only tg", p.srv.asked)
	}
}

// The feed's check is the panel's own test query, sent now.
func TestFeedCheckSendsATestQuery(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	p.minute(nil)
	p.dns.mu.Lock()
	p.dns.err["t3.example.com"] = errors.New("read udp 127.0.0.1:53: i/o timeout")
	p.dns.mu.Unlock()
	code, res := p.check("feed")
	if code != 200 || res.OK || res.Summary != "No answer to the test query on t3.example.com" {
		t.Errorf("feed check = %d %+v, want 200, not ok, no answer on t3", code, res)
	}
	if r := p.board().row("feed"); r.State != "down" {
		t.Errorf("feed after the check = %s, want down", r.State)
	}
	p.dns.mu.Lock()
	p.dns.err = nil
	p.dns.mu.Unlock()
	if _, res := p.check("feed"); !res.OK || res.Summary != "All 3 domains answered the test query" {
		t.Errorf("feed check = %+v, want ok, \"All 3 domains answered the test query\"", res)
	}
	p.at(t0.Add(2*time.Minute), nil)
	if _, res := p.check("feed"); res.OK || res.Summary != "thefeed-server stopped" {
		t.Errorf("feed check, server stopped = %+v, want not ok, \"thefeed-server stopped\"", res)
	}
}

// Fetch now asks for a fetch, and reports the first cycle that started
// after it, polling status until there is one.
func TestFetchNowReportsTheNextCycle(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	p.minute(nil)
	asked := t0.Add(time.Minute + 30*time.Second)
	p.at(asked, healthy(asked))
	code, res := p.check("chan")
	if code != 202 || !res.Pending || res.Summary != "Fetching…" || p.srv.refreshed != 1 {
		t.Fatalf("fetch now = %d %+v, refreshed %d; want 202, pending, Fetching…, once", code, res, p.srv.refreshed)
	}
	if d := p.feature("chan"); d.Checked == nil || !d.Checked.Pending {
		t.Errorf("checked = %+v, want pending", d.Checked)
	}
	// Still the same minute: a cycle that started after the request is back.
	back := asked.Add(20 * time.Second)
	st := healthy(back)
	st.Channels.Cycles = append(st.Channels.Cycles, controlapi.FetchCycle{Start: asked.Add(time.Second), DurationMs: 171000, Fetched: 42, Total: 42, Posts: 6})
	p.at(back, st)
	d := p.feature("chan")
	if c := d.Checked; c == nil || c.Pending || !c.OK || !c.At.Equal(back) || c.Summary != "42 of 42 channels fetched, 6 new posts." {
		t.Errorf("checked = %+v, want ok at 12:01:50, \"42 of 42 channels fetched, 6 new posts.\"", c)
	}
}

// After a restart there's no cycle yet: an empty table, and a Fetch now
// that gives up when none comes back within 3 intervals.
func TestFetchNowGivesUp(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	noCycles := func(st *controlapi.Status) { st.Channels.Cycles, st.Server.Started = nil, t0 }
	p.minute(noCycles)
	if got, want := js(p.feature("chan").Deps), `{"Title":"Recent cycles","Cols":["Started","Channels","Posts","Took"],"Rows":[]}`; got != want {
		t.Errorf("deps = %s, want %s", got, want)
	}
	if code, _ := p.check("chan"); code != 202 {
		t.Fatalf("fetch now = %d, want 202", code)
	}
	st := healthy(t0.Add(32 * time.Minute)) // asked at 12:01: 31 minutes on
	noCycles(st)
	p.at(t0.Add(32*time.Minute), st)
	if c := p.feature("chan").Checked; c == nil || c.Pending || c.OK || c.Summary != "No fetch cycle came back in 30 min." {
		t.Errorf("checked after 30 minutes = %+v, want not ok, \"No fetch cycle came back in 30 min.\"", c)
	}

	p.check("chan")
	p.minute(func(st *controlapi.Status) { st.Channels = nil })
	if c := p.feature("chan").Checked; c == nil || c.Pending || c.OK || c.Summary != "Telegram channels are turned off." {
		t.Errorf("checked when turned off = %+v, want not ok, \"Telegram channels are turned off.\"", c)
	}
}

// Samples are kept 7 days, incidents 90.
func TestOldHistoryIsDropped(t *testing.T) {
	p := newPanel(t, t0, healthy(t0))
	for _, change := range []func(*controlapi.Status){xDown, xDown, nil, nil} {
		p.minute(change)
	}
	day := 24 * time.Hour
	if d := p.feature("x"); len(d.Incidents) != 1 || !d.Since.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("x = %d incidents, ok since %v; want 1, 12:03", len(d.Incidents), d.Since)
	}
	later := t0.Add(89 * day)
	p.at(later, healthy(later))
	// Ok all along, but samples only go back 7 days.
	if d := p.feature("x"); len(d.Incidents) != 1 || !d.Since.Equal(later) {
		t.Errorf("after 89 days, x = %d incidents, ok since %v; want 1, since now", len(d.Incidents), d.Since)
	}
	later = t0.Add(91 * day)
	p.at(later, healthy(later))
	if d := p.feature("x"); len(d.Incidents) != 0 {
		t.Errorf("after 91 days, incidents = %+v, want none", d.Incidents)
	}
}
