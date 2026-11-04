package logs_test

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sartoopjj/thefeed/internal/admin/dockerapi"
	"github.com/sartoopjj/thefeed/internal/admin/logs"
)

// engine plays the Docker Engine API on a unix socket: the container
// thefeed-server and its log as the json-file driver keeps it, one message
// per line written, oldest first.
type engine struct {
	tty  bool
	log  []message
	sock string
}

type message struct {
	at     string // as the Engine prints it with timestamps=1
	stream byte   // 1 stdout, 2 stderr
	text   string // with its newline
}

func newEngine(t *testing.T, log ...message) *engine {
	t.Helper()
	dir, err := os.MkdirTemp("", "d") // macOS caps a socket path at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := &engine{log: log, sock: filepath.Join(dir, "s")}
	l, err := net.Listen("unix", e.sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(e.handler())
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return e
}

// panel is the admin app with the logs module on this engine's container.
func (e *engine) panel(t *testing.T) *panel {
	return newPanel(t, logs.Container(dockerapi.New(e.sock), "thefeed-server"))
}

func (e *engine) handler() http.Handler {
	mux := http.NewServeMux()
	known := func(w http.ResponseWriter, r *http.Request) bool {
		if r.PathValue("name") != "thefeed-server" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"message": "No such container: " + r.PathValue("name")})
			return false
		}
		return true
	}
	mux.HandleFunc("GET /containers/{name}/json", func(w http.ResponseWriter, r *http.Request) {
		if known(w, r) {
			json.NewEncoder(w).Encode(map[string]any{"Id": "4f1c2a9b3d5e", "Name": "/thefeed-server", "Config": map[string]any{"Tty": e.tty}})
		}
	})
	// Logs: the last tail messages, each in its own frame (stream, three
	// zero bytes, the payload's size, the payload) without a TTY, as is
	// with one.
	mux.HandleFunc("GET /containers/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		if !known(w, r) {
			return
		}
		q := r.URL.Query()
		msgs := e.log
		if n, err := strconv.Atoi(q.Get("tail")); err == nil {
			msgs = msgs[max(0, len(msgs)-n):]
		}
		for _, m := range msgs {
			if q.Get(map[byte]string{1: "stdout", 2: "stderr"}[m.stream]) != "1" {
				continue
			}
			p := m.text
			if q.Get("timestamps") == "1" {
				p = m.at + " " + p
			}
			if !e.tty {
				w.Write(binary.BigEndian.AppendUint32([]byte{m.stream, 0, 0, 0}, uint32(len(p))))
			}
			w.Write([]byte(p))
		}
	})
	return mux
}

func TestDockerStdoutAndStderrInterleaved(t *testing.T) {
	e := newEngine(t,
		message{"2026-11-03T23:59:58.000561123Z", 2, "2026/11/03 23:59:58 [chat] sweep: expired 3 message(s), deleted 0 account(s)\n"},
		message{"2026-11-04T00:00:01.250000000Z", 1, "thefeed-server v0.41.0 (commit: 3f2a1bc, built: 2026-11-01)\n"},
		message{"2026-11-04T00:00:01.250431907Z", 2, "2026/11/04 00:00:01 [dns] listening on :5300 (udp)\n"},
		message{"2026-11-04T12:01:29.900214123Z", 2, "2026/11/04 12:01:29 [x] fetch @jack: all instances failed: nitter.net: HTTP 429 Too Many Requests\n"},
	)
	got := e.panel(t).page("/api/logs/server")
	want := page(t, `{"days":[
		{"day":"2026-11-04","lines":[
			{"at":"2026-11-04T12:01:29.900214Z","level":"ERROR","feature":"x","text":"[x] fetch @jack: all instances failed: nitter.net: HTTP 429 Too Many Requests"},
			{"at":"2026-11-04T00:00:01.250431Z","level":"INFO","feature":"feed","text":"[dns] listening on :5300 (udp)"},
			{"at":"2026-11-04T00:00:01.25Z","level":"INFO","feature":"","text":"thefeed-server v0.41.0 (commit: 3f2a1bc, built: 2026-11-01)"}]},
		{"day":"2026-11-03","lines":[
			{"at":"2026-11-03T23:59:58.000561Z","level":"INFO","feature":"chat","text":"[chat] sweep: expired 3 message(s), deleted 0 account(s)"}]}],
		"next":""}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("server log =\n%+v\nwant\n%+v", got, want)
	}
}

// Docker keeps a line over 16 KiB as messages of 16 KiB: each one with the
// line's time, and only the last one with the newline. The other stream
// can write in between.
func TestDockerLineSplitAcrossTwoFrames(t *testing.T) {
	report := `[dns_hourly] {"channels":[{"channel":1,"name":"bbcpersian","queries":` + strings.Repeat("9", 16<<10) + `}]}`
	e := newEngine(t,
		message{"2026-11-04T00:00:00.004213350Z", 2, "2026/11/04 00:00:00 " + report[:16<<10-20]},
		message{"2026-11-04T00:00:00.006000000Z", 1, "thefeed-server v0.41.0 (commit: 3f2a1bc, built: 2026-11-01)\n"},
		message{"2026-11-04T00:00:00.004213350Z", 2, report[16<<10-20:] + "\n"},
		message{"2026-11-04T00:00:00.010092004Z", 2, "2026/11/04 00:00:00 [chat] rotated server encryption key\n"},
	)
	p := e.panel(t)
	got := p.page("/api/logs/feed").lines()
	if len(got) != 1 || got[0].At != "2026-11-04T00:00:00.004213Z" || got[0].Text != report {
		t.Errorf("feed log = %.80q, want the whole report at 00:00:00.004213", got)
	}
	want := []string{"[chat] rotated server encryption key", "thefeed-server v0.41.0 (commit: 3f2a1bc, built: 2026-11-01)", report}
	if got := texts(p.page("/api/logs/server").lines()); !slices.Equal(got, want) {
		t.Errorf("server log = %.80q, want %.80q", got, want)
	}

	// The feed page scans back in windows of 4 messages: the first holds
	// only the report's second part.
	e.log = []message{
		{"2026-11-04T00:00:00.004213350Z", 2, "2026/11/04 00:00:00 " + report[:16<<10-20]},
		{"2026-11-04T00:00:00.004213350Z", 2, report[16<<10-20:] + "\n"},
		stderr("2026-11-04T00:00:01.000000000Z", "[x] fetch cycle started for 6 accounts"),
		stderr("2026-11-04T00:00:02.000000000Z", "[chat] rotated server encryption key"),
		stderr("2026-11-04T00:00:03.000000000Z", "[x] reader goroutine started"),
	}
	if got := p.pages("/api/logs/feed?limit=1"); !slices.EqualFunc(got, [][]string{{report}, nil}, slices.Equal) {
		t.Errorf("feed pages of 1 = %.80q, want the whole report, then nothing", got)
	}
}

// stderr is a line thefeed-server logs at, as Go's log package writes it.
func stderr(at, text string) message {
	return message{at, 2, "2026/11/04 09:00:00 " + text + "\n"}
}

// cycle is a fetch cycle's log. Lines can share a time; three do here,
// across the edge of the 3rd and 4th pages of 2.
func cycle(t *testing.T) *engine {
	return newEngine(t,
		stderr("2026-11-04T09:00:00.000000000Z", "[telegram] fetch cycle started for 40 channels"),
		stderr("2026-11-04T09:00:01.500000000Z", "[telegram] updated @bbcpersian: 3 new"),
		stderr("2026-11-04T09:00:02.250000000Z", "[telegram] updated @varzesh3: 1 new"),
		stderr("2026-11-04T09:00:03.123456789Z", "[gh-relay] committed 2 file(s)"),
		stderr("2026-11-04T09:00:03.123456789Z", "[gh-relay] committed 1 file(s)"),
		stderr("2026-11-04T09:00:03.123456789Z", "[gh-relay] owner/relay-repo is reachable again"),
		stderr("2026-11-04T09:00:04.000000000Z", "[telegram] fetch @bbcpersian: get history failed: FLOOD_WAIT_7"),
		stderr("2026-11-04T09:00:05.000000000Z", "[x] fetch cycle started for 6 accounts"),
		stderr("2026-11-04T09:00:06.000000000Z", "[x] @jack: instance nitter.net: HTTP 429 Too Many Requests"),
		stderr("2026-11-04T09:00:07.000000000Z", "[chat] rotated server encryption key"),
		stderr("2026-11-04T09:01:20.000000000Z", "[telegram] fetch cycle done in 1m20s: 40 fetched, 2 failed"),
	)
}

func TestDockerPagingFollowsTheCursor(t *testing.T) {
	got := cycle(t).panel(t).pages("/api/logs/server?limit=2")
	want := [][]string{
		{"[telegram] fetch cycle done in 1m20s: 40 fetched, 2 failed", "[chat] rotated server encryption key"},
		{"[x] @jack: instance nitter.net: HTTP 429 Too Many Requests", "[x] fetch cycle started for 6 accounts"},
		{"[telegram] fetch @bbcpersian: get history failed: FLOOD_WAIT_7", "[gh-relay] owner/relay-repo is reachable again"},
		{"[gh-relay] committed 1 file(s)", "[gh-relay] committed 2 file(s)"},
		{"[telegram] updated @varzesh3: 1 new", "[telegram] updated @bbcpersian: 3 new"},
		{"[telegram] fetch cycle started for 40 channels"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("pages =\n%q\nwant\n%q", got, want)
	}
}

func TestDockerFeatureLogAndWarningsFilter(t *testing.T) {
	p := cycle(t).panel(t)
	for path, want := range map[string][][]string{
		"/api/logs/chan?limit=2": {
			{"[telegram] fetch cycle done in 1m20s: 40 fetched, 2 failed", "[telegram] fetch @bbcpersian: get history failed: FLOOD_WAIT_7"},
			{"[telegram] updated @varzesh3: 1 new", "[telegram] updated @bbcpersian: 3 new"},
			{"[telegram] fetch cycle started for 40 channels"},
		},
		"/api/logs/chan?limit=2&filter=bad": {{"[telegram] fetch @bbcpersian: get history failed: FLOOD_WAIT_7"}},
		"/api/logs/x?limit=2&filter=bad":    {{"[x] @jack: instance nitter.net: HTTP 429 Too Many Requests"}},
		"/api/logs/gh?limit=2&filter=all": {
			{"[gh-relay] owner/relay-repo is reachable again", "[gh-relay] committed 1 file(s)"},
			{"[gh-relay] committed 2 file(s)"},
		},
	} {
		if got := p.pages(path); !slices.EqualFunc(got, want, slices.Equal) {
			t.Errorf("%s =\n%q\nwant\n%q", path, got, want)
		}
	}
}

// With a TTY (tty: true in compose), the log is plain lines, all on
// stdout, each ending in \r\n.
func TestDockerContainerWithATTY(t *testing.T) {
	e := newEngine(t,
		message{"2026-11-04T09:00:00.000000000Z", 1, "2026/11/04 09:00:00 [mtproto] dial dc2 -> 149.154.167.51:443 ok\r\n"},
		message{"2026-11-04T09:00:30.000000000Z", 1, "2026/11/04 09:00:30 [mtproto] dial dc4: dial tcp 149.154.167.91:443: i/o timeout\r\n"},
	)
	e.tty = true
	got := e.panel(t).page("/api/logs/tg").lines()
	want := []logLine{
		{"2026-11-04T09:00:30Z", "WARN", "tg", "[mtproto] dial dc4: dial tcp 149.154.167.91:443: i/o timeout"},
		{"2026-11-04T09:00:00Z", "INFO", "tg", "[mtproto] dial dc2 -> 149.154.167.51:443 ok"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("tg log =\n%q\nwant\n%q", got, want)
	}
}

func TestDockerEngineUnreachableOrNoContainer(t *testing.T) {
	e := cycle(t)
	for _, c := range []struct {
		j    logs.Journal
		want string
	}{
		{logs.Container(dockerapi.New(filepath.Join(filepath.Dir(e.sock), "gone")), "thefeed-server"), "connect: no such file or directory"},
		{logs.Container(dockerapi.New(e.sock), "thefeed"), "No such container: thefeed"},
	} {
		w := newPanel(t, c.j).get("/api/logs/server")
		if w.Code != 503 || !strings.Contains(w.Body.String(), `"message":"Couldn't read thefeed-server's journal: `) || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("GET /api/logs/server = %d %s, want 503 saying %q", w.Code, w.Body, c.want)
		}
	}
}

// page is a page of the logs API as JSON.
func page(t *testing.T, s string) logPage {
	t.Helper()
	var p logPage
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
