package logs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/logs"
)

// fakeJournal serves testdata/journal.json, recorded as `journalctl -o json`
// prints it (oldest first), the way `journalctl -r --cursor=C -n N` would:
// newest first, starting at the cursor's own entry.
type fakeJournal struct {
	lines   [][]byte // newest first
	cursors []string
	err     error
}

func loadJournal(t *testing.T) *fakeJournal {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	j := &fakeJournal{}
	for _, l := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var e struct {
			Cursor string `json:"__CURSOR"`
		}
		if err := json.Unmarshal(l, &e); err != nil {
			t.Fatal(err)
		}
		j.lines = append(j.lines, l)
		j.cursors = append(j.cursors, e.Cursor)
	}
	slices.Reverse(j.lines)
	slices.Reverse(j.cursors)
	return j
}

func (j *fakeJournal) Read(_ context.Context, cursor string, n int) ([]byte, error) {
	if j.err != nil {
		return nil, j.err
	}
	start := 0
	if cursor != "" {
		if start = slices.Index(j.cursors, cursor); start < 0 {
			return nil, fmt.Errorf("Failed to seek to cursor: Invalid argument")
		}
	}
	return bytes.Join(j.lines[start:min(start+n, len(j.lines))], []byte("\n")), nil
}

type logLine struct {
	At, Level, Feature, Text string
}

type logPage struct {
	Days []struct {
		Day   string
		Lines []logLine
	}
	Next string
}

// lines flattens the page, newest first.
func (p logPage) lines() []logLine {
	var out []logLine
	for _, d := range p.Days {
		out = append(out, d.Lines...)
	}
	return out
}

type panel struct {
	t      *testing.T
	app    *admin.App
	cookie *http.Cookie
}

const origin = "https://panel.test"

// newPanel is the real admin app with the logs module on j, signed in.
func newPanel(t *testing.T, j logs.Journal) *panel {
	t.Helper()
	now := time.Date(2026, 11, 4, 12, 5, 0, 0, time.UTC)
	cfg := admin.Config{AdminDir: t.TempDir(), Clock: admin.ClockFunc(func() time.Time { return now })}
	app, err := admin.New(cfg, logs.New(j))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	p := &panel{t: t, app: app}
	code, err := os.ReadFile(filepath.Join(cfg.AdminDir, "setup-code"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(string(code)), "username": "admin", "password": "correct horse battery", "repeat": "correct horse battery"})
	req := httptest.NewRequest("POST", origin+"/api/auth/setup", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, req)
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

func (p *panel) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", origin+path, nil)
	if p.cookie != nil {
		req.AddCookie(p.cookie)
	}
	w := httptest.NewRecorder()
	p.app.Handler().ServeHTTP(w, req)
	return w
}

func (p *panel) page(path string) logPage {
	p.t.Helper()
	w := p.get(path)
	if w.Code != 200 {
		p.t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
	}
	var out logPage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		p.t.Fatalf("GET %s: %v in %s", path, err, w.Body)
	}
	return out
}

func TestServerLogIsNewestFirstAndGroupedByDay(t *testing.T) {
	p := newPanel(t, loadJournal(t))
	got := p.page("/api/logs/server?limit=500")

	if len(got.Days) != 2 || got.Days[0].Day != "2026-11-04" || got.Days[1].Day != "2026-11-03" {
		t.Fatalf("days = %+v, want 2026-11-04 then 2026-11-03", got.Days)
	}
	today, yesterday := got.Days[0].Lines, got.Days[1].Lines
	if len(today) != 27 || len(yesterday) != 16 {
		t.Fatalf("lines per day = %d, %d; want 27, 16", len(today), len(yesterday))
	}
	if got.Next != "" {
		t.Errorf("next = %q at the start of the journal, want empty", got.Next)
	}
	// Times come from the journal; the "2026/11/04 12:01:29 " that Go's log
	// writes in front of each line is not part of the text.
	for i, want := range []logLine{
		{At: "2026-11-04T12:01:30.001336Z", Text: "thefeed-server.service: Failed with result 'exit-code'."},
		{At: "2026-11-04T12:01:30.000874Z", Text: "thefeed-server.service: Main process exited, code=exited, status=1/FAILURE"},
		{At: "2026-11-04T12:01:29.900214Z", Text: "Server error: listen udp :53: bind: address already in use"},
	} {
		if today[i].At != want.At || today[i].Text != want.Text {
			t.Errorf("today[%d] = %s %q, want %s %q", i, today[i].At, today[i].Text, want.At, want.Text)
		}
	}
	// Around midnight.
	if l := today[len(today)-1]; l.At != "2026-11-04T00:00:00.004213Z" || !strings.HasPrefix(l.Text, `[dns_hourly] {"channels":[{"channel":1,"name":"bbcpersian"`) {
		t.Errorf("first line of 4 Nov = %s %q", l.At, l.Text)
	}
	if l := yesterday[0]; l.At != "2026-11-03T23:59:58.000561Z" || l.Text != "[chat] sweep: expired 3 message(s), deleted 0 account(s)" {
		t.Errorf("last line of 3 Nov = %s %q", l.At, l.Text)
	}
	if l := yesterday[len(yesterday)-1]; l.Text != "Started thefeed-server.service - thefeed server." {
		t.Errorf("oldest line = %q", l.Text)
	}
}

// find returns the line whose text starts with prefix.
func find(t *testing.T, lines []logLine, prefix string) logLine {
	t.Helper()
	for _, l := range lines {
		if strings.HasPrefix(l.Text, prefix) {
			return l
		}
	}
	t.Fatalf("no line starting %q", prefix)
	return logLine{}
}

// Every prefix internal/server logs with, and the feature page it belongs on
// (the mockup's feature ids). "" is the server log only.
func TestEveryPrefixMapsToItsFeature(t *testing.T) {
	p := newPanel(t, loadJournal(t))
	lines := p.page("/api/logs/server?limit=500").lines()
	for _, c := range []struct{ prefix, feature string }{
		{"[dns] listening on", "feed"},
		{"[dns_hourly] ", "feed"},
		{"[extrablock] ", "feed"},
		{"[meta] ", "feed"},
		{"[telegram] fetch cycle done", "chan"},
		{"[telegram] updated @varzesh3", "chan"}, // MESSAGE arrived as a byte array: not valid UTF-8
		{"[public] ", "chan"},
		{"[admin] added channel", "chan"},
		{"[profile-pics] ", "chan"},
		{"[public profile-pic] ", "chan"},
		{"[x] fetch @jack", "x"},
		{"[x profile-pic] ", "x"},
		{"[mtproto] dial dc2", "tg"},
		{"[chat] rotated", "chat"},
		{"[media-debug] media: store", "dnsm"},
		{"[media-debug] [media-http] ", "dnsm"},
		{"[gh-relay] committed", "gh"},
		{"[server] read 24", ""},
		{"[server] chat enabled", ""},
		{"[version] ", ""},
		{"Starting thefeed server", ""},
		{"thefeed-server.service: Main process exited", ""},
	} {
		if got := find(t, lines, c.prefix).Feature; got != c.feature {
			t.Errorf("%q: feature = %q, want %q", c.prefix, got, c.feature)
		}
	}
}

func texts(lines []logLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

func TestFeatureLogAndWarningsFilter(t *testing.T) {
	p := newPanel(t, loadJournal(t))
	all := []string{
		"[x profile-pic] @jack: unexpected EOF",
		"[x] fetch @jack: all instances failed: nitter.poast.org: dial tcp: lookup nitter.poast.org: no such host",
		`[x] @jack: instance nitter.poast.org: network error: Get "https://nitter.poast.org/jack/rss": dial tcp: lookup nitter.poast.org: no such host`,
		`[x] @jack: instance xcancel.com: network error: Get "https://xcancel.com/jack/rss": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`,
		"[x] @jack: instance nitter.net: HTTP 429 Too Many Requests",
		"[x] fetch cycle started for 6 accounts (instances: [nitter.net xcancel.com nitter.poast.org])",
		"[x] reader goroutine started",
	}
	got := p.page("/api/logs/x")
	if !slices.Equal(texts(got.lines()), all) {
		t.Errorf("x log =\n%s\nwant\n%s", strings.Join(texts(got.lines()), "\n"), strings.Join(all, "\n"))
	}
	if len(got.Days) != 2 || len(got.Days[0].Lines) != 1 || got.Days[1].Day != "2026-11-03" {
		t.Errorf("x log days = %+v, want 1 line on 4 Nov then 6 on 3 Nov", got.Days)
	}
	if got := texts(p.page("/api/logs/x?filter=bad").lines()); !slices.Equal(got, all[:5]) {
		t.Errorf("x warnings and errors =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(all[:5], "\n"))
	}
	if got := texts(p.page("/api/logs/x?filter=all").lines()); !slices.Equal(got, all) {
		t.Errorf("filter=all = %q, want every x line", got)
	}
}

// pages follows next from path until it runs out, returning each page's texts.
func (p *panel) pages(path string) [][]string {
	p.t.Helper()
	var out [][]string
	got := p.page(path)
	for {
		out = append(out, texts(got.lines()))
		if got.Next == "" || len(out) > 50 {
			return out
		}
		got = p.page(path + "&cursor=" + url.QueryEscape(got.Next))
	}
}

func TestPagingFollowsTheCursor(t *testing.T) {
	p := newPanel(t, loadJournal(t))
	// The 5 chat lines are spread over the journal's 43, far apart.
	cell := "[chat] cell sel=3f ctr=12 dom=c.example.com name=abc.c.example.com from=203.0.113.9"
	flush := "[chat] flush: dirty account 7f3e missing from cache"
	sync := "[chat] 2a1b9c store sync: write /opt/thefeed/data/chat/2a1b9c.db: no space left on device"
	sweep := "[chat] sweep: expired 3 message(s), deleted 0 account(s)"
	rotated := "[chat] rotated server encryption key"

	got := p.pages("/api/logs/chat?limit=2")
	if want := [][]string{{cell, flush}, {sync, sweep}, {rotated}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("chat pages = %q, want %q", got, want)
	}
	// The last warning is followed by a page with nothing left in it.
	got = p.pages("/api/logs/chat?limit=1&filter=bad")
	if want := [][]string{{flush}, {sync}, nil}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("chat warning pages = %q, want %q", got, want)
	}
	got = p.pages("/api/logs/server?limit=20")
	if len(got) != 3 || len(got[0]) != 20 || len(got[1]) != 20 || len(got[2]) != 3 {
		t.Fatalf("server log pages of 20 = %d pages %v", len(got), got)
	}
	if got[1][0] != "[x profile-pic] @jack: unexpected EOF" || got[2][2] != "Started thefeed-server.service - thefeed server." {
		t.Errorf("page 2 starts %q, page 3 ends %q", got[1][0], got[2][2])
	}
}

func TestRefusals(t *testing.T) {
	j := loadJournal(t)
	p := newPanel(t, j)
	for _, c := range []struct {
		path  string
		code  int
		field string
	}{
		{"/api/logs/nope", 404, "feature"},
		{"/api/logs/x?filter=errors", 400, "filter"},
	} {
		w := p.get(c.path)
		if w.Code != c.code || !strings.Contains(w.Body.String(), `"field":"`+c.field+`"`) {
			t.Errorf("GET %s = %d %s, want %d on %s", c.path, w.Code, w.Body, c.code, c.field)
		}
	}
	j.err = errors.New(`exec: "journalctl": executable file not found in $PATH`)
	if w := p.get("/api/logs/server"); w.Code != 503 || !strings.Contains(w.Body.String(), "journalctl") {
		t.Errorf("journal failing = %d %s, want 503 saying why", w.Code, w.Body)
	}
	p.cookie = nil
	if w := p.get("/api/logs/server"); w.Code != 401 {
		t.Errorf("signed out = %d, want 401", w.Code)
	}
}

// Levels as an operator reads these real lines: ERROR when something failed,
// stopped or was lost; WARN when it was refused, retried or is degrading.
func TestLevelsOfRealLines(t *testing.T) {
	p := newPanel(t, loadJournal(t))
	lines := p.page("/api/logs/server?limit=500").lines()
	for _, c := range []struct{ prefix, level string }{
		// Summaries, reports and recoveries that mention failure words.
		{"[telegram] fetch cycle done in 1m20s: 40 fetched, 2 failed", "INFO"},
		{"[public] fetch cycle done", "INFO"},
		{"[profile-pics] cycle done: 2 changed, 0 dropped", "INFO"},
		{"[dns_hourly] {", "INFO"}, // carries tokenDenied, storeRejected, evictions, totalInvalidQueries
		{"[gh-relay] owner/relay-repo is reachable again", "INFO"},
		{"[x] fetch cycle started", "INFO"},
		{"[server] chat enabled", "INFO"},
		{"[mtproto] dial dc2 -> 149.154.167.51:443 ok", "INFO"},
		// Channel names and titles are anyone's words: "Bad Calls", @lostfilm.
		{"[telegram] updated @varzesh3", "INFO"},
		{"[admin] added channel @lostfilm", "INFO"},
		{"Started thefeed-server.service", "INFO"},
		// One Nitter instance failing is a warning; all of them is the error.
		{"[x] @jack: instance nitter.net: HTTP 429", "WARN"},
		{"[x] @jack: instance xcancel.com: network error", "WARN"},
		{"[x] fetch @jack: all instances failed", "ERROR"},
		{"[mtproto] dial dc4: dial tcp 149.154.167.91:443: i/o timeout", "WARN"},
		{"[mtproto] handshake table full (256): evicted the oldest", "WARN"},
		{"[gh-relay] GitHub asked to slow down", "WARN"},
		{"[gh-relay] repo owner/relay-repo is 92% of its 5 GB size budget", "WARN"},
		{"[dns] admin query rejected", "WARN"},
		{"[chat] flush: dirty account 7f3e missing from cache", "WARN"},
		{"[public profile-pic] @durov", "WARN"},
		{"[x profile-pic] @jack: unexpected EOF", "WARN"},
		{"[media-debug] [media-http] ", "WARN"},
		{"[extrablock] channel 7: extrablock: bad private key size 12", "ERROR"},
		{"[meta] EncodeMetadataExtended failed", "ERROR"},
		{"[chat] 2a1b9c store sync: write /opt/thefeed/data/chat/2a1b9c.db: no space left on device", "ERROR"},
		{"[gh-relay] TOKEN CANNOT WRITE", "ERROR"},
		{"[telegram] fetch @bbcpersian: get history failed", "ERROR"},
		{"Server error: listen udp :53", "ERROR"},
		{"thefeed-server.service: Main process exited, code=exited, status=1/FAILURE", "ERROR"},
		{"thefeed-server.service: Failed with result 'exit-code'.", "ERROR"},
	} {
		if got := find(t, lines, c.prefix).Level; got != c.level {
			t.Errorf("%q: level = %q, want %q", c.prefix, got, c.level)
		}
	}
}
