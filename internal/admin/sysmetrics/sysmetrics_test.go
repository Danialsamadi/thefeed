package sysmetrics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/miekg/dns"
	bolt "go.etcd.io/bbolt"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/sysmetrics"
	"github.com/sartoopjj/thefeed/internal/controlapi"
)

const (
	origin  = "https://panel.test"
	dataDir = "/srv/thefeed/data"
)

// rig is the real admin app with the sysmetrics module on a faked machine:
// /proc from testdata in a MapFS the test edits between samples, statfs,
// the clock and its timers, DNS exchanges and thefeed-server's control API.
type rig struct {
	t           *testing.T
	now         time.Time
	root        fstest.MapFS
	total, free uint64             // what statfs says about the data directory
	status      *controlapi.Status // nil: thefeed-server doesn't answer
	rcode       map[string]int     // each resolver's answer; missing ones time out
	asked       []string           // "resolver name", in order
	timers      map[time.Duration]func()
	user, idle  uint64 // CPU ticks so far, for step
	rx, tx      uint64 // eth0's bytes so far, for step
	adminDir    string
	app         *admin.App
	cookie      *http.Cookie
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		t:     t,
		now:   time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		root:  fstest.MapFS{},
		total: 20_000_000_000, free: 12_400_000_000,
		status: &controlapi.Status{
			Server: controlapi.Server{
				Version: "v0.41.0", Started: time.Date(2026, 10, 22, 7, 58, 0, 0, time.UTC), Answering: true,
				Config: map[string]string{"dns.domain": "t.example.com", "server.listen": ":5300"},
			},
			MediaCache: &controlapi.MediaCacheStats{Entries: 120, Bytes: 4_100_000_000, RAMBytes: 98 << 20},
			GHRelay:    &controlapi.RelayStatus{Repo: "op/media"},
		},
		rcode:    map[string]int{},
		adminDir: t.TempDir(),
		// testdata/proc's counters: 1,500 busy and 8,100 idle CPU ticks.
		user: 1500, idle: 8100, rx: 1_000_000, tx: 2_000_000,
	}
	err := fs.WalkDir(os.DirFS("testdata"), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(filepath.Join("testdata", p))
		r.root[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// thefeed-server's open files: 8 descriptors, 3 of them sockets.
	for fd, target := range []string{"/dev/null", "/dev/null", "/dev/null", "socket:[81234]", "socket:[81235]",
		"anon_inode:[eventpoll]", "socket:[81240]", dataDir + "/chat.db"} {
		r.root["proc/4242/fd/"+string(rune('0'+fd))] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte(target)}
	}
	r.start()
	return r
}

// start opens the panel on r.adminDir and signs in.
func (r *rig) start() {
	t := r.t
	t.Helper()
	r.timers = map[time.Duration]func(){}
	m := sysmetrics.Machine{
		Root: r.root,
		Statfs: func(path string) (total, free uint64, err error) {
			if path != dataDir {
				return 0, 0, errors.New("statfs " + path + ": not the data directory")
			}
			return r.total, r.free, nil
		},
		Exchange: func(_ context.Context, q *dns.Msg, server string) (*dns.Msg, error) {
			r.asked = append(r.asked, server+" "+q.Question[0].Name)
			rc, ok := r.rcode[server]
			if !ok {
				return nil, errors.New("read udp: i/o timeout")
			}
			a := new(dns.Msg)
			a.SetRcode(q, rc)
			return a, nil
		},
		Every:     func(every time.Duration, f func()) { r.timers[every] = f },
		Server:    fakeServer{r},
		Resolvers: m4,
	}
	cfg := admin.Config{AdminDir: r.adminDir, ServerDataDir: dataDir, Clock: admin.ClockFunc(func() time.Time { return r.now })}
	app, err := admin.New(cfg, sysmetrics.New(m))
	if err != nil {
		t.Fatal(err)
	}
	r.app = app
	t.Cleanup(func() { app.Close() })
	r.signIn()
}

// signIn creates the admin on first run and signs in.
func (r *rig) signIn() {
	t := r.t
	t.Helper()
	code, err := os.ReadFile(filepath.Join(r.adminDir, "setup-code"))
	path, body := "/api/auth/setup", map[string]string{"code": strings.TrimSpace(string(code)), "username": "admin", "password": "correct horse battery", "repeat": "correct horse battery"}
	if errors.Is(err, fs.ErrNotExist) {
		path, body = "/api/auth/login", map[string]string{"username": "admin", "password": "correct horse battery"}
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", origin+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(w, req)
	r.cookie = nil
	for _, c := range w.Result().Cookies() {
		if c.Name == "thefeed_admin" && c.Value != "" {
			r.cookie = c
		}
	}
	if r.cookie == nil {
		t.Fatalf("%s = %d %s", path, w.Code, w.Body)
	}
}

type fakeServer struct{ r *rig }

func (f fakeServer) Status(context.Context) (*controlapi.Status, error) {
	if f.r.status == nil {
		return nil, errors.New("dial unix " + dataDir + "/control.sock: connect: connection refused")
	}
	return f.r.status, nil
}
func (fakeServer) ReloadChannels(context.Context) error     { return errors.New("not used") }
func (fakeServer) Refresh(context.Context) error            { return errors.New("not used") }
func (fakeServer) SetAnswering(context.Context, bool) error { return errors.New("not used") }

// sample takes one 10-second sample at the current time.
func (r *rig) sample() { r.fire(10 * time.Second) }

// probe runs the 15-minute resolver probe.
func (r *rig) probe() { r.fire(15 * time.Minute) }

func (r *rig) fire(every time.Duration) {
	r.t.Helper()
	f := r.timers[every]
	if f == nil {
		r.t.Fatalf("nothing runs every %s", every)
	}
	f()
}

// line replaces the line of file that starts with prefix (ignoring leading spaces).
func (r *rig) line(file, prefix, with string) {
	r.t.Helper()
	lines := strings.Split(string(r.root[file].Data), "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " "), prefix) {
			lines[i] = with
			r.root[file] = &fstest.MapFile{Data: []byte(strings.Join(lines, "\n"))}
			return
		}
	}
	r.t.Fatalf("%s has no line starting %q", file, prefix)
}

// after moves the clock on by d and takes the next sample, with the CPU
// line and eth0's and wg0's counters as given.
func (r *rig) after(d time.Duration, cpu string, eth0RX, eth0TX, wg0 int) {
	r.t.Helper()
	r.now = r.now.Add(d)
	r.line("proc/stat", "cpu ", "cpu  "+cpu)
	r.line("proc/net/dev", "eth0:", fmt.Sprintf("  eth0: %d    2000    0    0    0     0          0         0 %d    3000    0    0    0     0       0          0", eth0RX, eth0TX))
	r.line("proc/net/dev", "wg0:", fmt.Sprintf("   wg0: %d    700    0    0    0     0          0         0 %d    600    0    0    0     0       0          0", wg0, wg0))
	r.sample()
}

// step moves the clock on by d and takes the next sample after the CPU spent
// busy and idle ticks and eth0 received rx and sent tx bytes.
func (r *rig) step(d time.Duration, busy, idle, rx, tx uint64) {
	r.t.Helper()
	r.user, r.idle, r.rx, r.tx = r.user+busy, r.idle+idle, r.rx+rx, r.tx+tx
	r.after(d, fmt.Sprintf("%d 0 0 %d 0 0 0 0 0 0", r.user, r.idle), int(r.rx), int(r.tx), 9_000_000)
}

type pinned struct {
	CPU, Out          *float64
	RAMUsed, DiskUsed int64
}

func TestPinnedValuesFromTwoSamples(t *testing.T) {
	r := newRig(t)
	r.sample()
	var got pinned
	r.getJSON("/api/server/pinned", &got)
	// RAM: 1,048,576 kB total − 626,688 kB available = 421,888 kB.
	// Disk: 20 GB − 12.4 GB free.
	if got.CPU != nil || got.Out != nil || got.RAMUsed != 432_013_312 || got.DiskUsed != 7_600_000_000 {
		t.Errorf("after one sample = cpu %v, out %v, ram %d, disk %d; want no CPU or out yet, ram 432013312, disk 7600000000",
			got.CPU, got.Out, got.RAMUsed, got.DiskUsed)
	}

	// 10 s on: user +150 (of which guest +100, already inside user), system +50,
	// idle +280, iowait +20. eth0 sends 3,000,000 bytes; wg0, which isn't the
	// default route, sends far more.
	r.after(10*time.Second, "1150 0 550 8280 120 0 0 0 100 0", 2_250_000, 5_000_000, 99_000_000)
	got = pinned{}
	r.getJSON("/api/server/pinned", &got)
	// CPU: 200 busy of 500 ticks = 40%. Out: 3,000,000 bytes × 8 / 10 s = 2.4 Mb/s.
	if got.CPU == nil || *got.CPU != 40 || got.Out == nil || *got.Out != 2.4 {
		t.Errorf("after two samples = cpu %v, out %v; want 40, 2.4", ptr(got.CPU), ptr(got.Out))
	}
}

// page is GET /api/server.
type page struct {
	Range string
	From  time.Time
	Step  int
	CPU   struct {
		Now, Avg, Peak *float64
		Points         []*float64
	}
	Net struct {
		OutNow, InNow, OutPeak   *float64
		Scale                    float64
		SentToday, ReceivedToday int64
		Out, In                  []*float64
	}
	RAM   struct{ Total, Server, MediaCache, System, Free int64 }
	Disk  struct{ Total, ReportsLogs, SystemData, Free int64 }
	Reach struct {
		Resolvers *struct {
			Answered, Asked int
			At              time.Time
		}
		Telegram, Nitter json.RawMessage
		GitHub           *struct {
			OK     bool
			Detail string
		}
	}
	Process struct {
		Running          bool
		Since            *time.Time
		Load             []float64
		FDs, Sockets     *int
		Listen, Redirect string
	}
}

func TestRAMDiskAndProcess(t *testing.T) {
	r := newRig(t)
	// In the data directory: 900 bytes of hourly reports and a 5,000-byte
	// chat store; in the journal, 100 bytes.
	r.root["srv/thefeed/data/dns_hourly.jsonl"] = &fstest.MapFile{Data: make([]byte, 600)}
	r.root["srv/thefeed/data/dns_hourly.jsonl.1"] = &fstest.MapFile{Data: make([]byte, 300)}
	r.root["srv/thefeed/data/chat.db"] = &fstest.MapFile{Data: make([]byte, 5000)}
	r.root["var/log/journal/0a1b2c/system.journal"] = &fstest.MapFile{Data: make([]byte, 100)}
	r.sample()
	var got page
	r.getJSON("/api/server", &got)

	// RAM, in bytes: 1,024 MiB total, 612 MiB available, thefeed-server's
	// RSS 384 MiB of which the media cache holds 98 MiB.
	// thefeed-server 384 − 98 = 286 MiB, system 1,024 − 612 − 384 = 28 MiB.
	ram := got.RAM
	if ram.Total != 1024<<20 || ram.Server != 286<<20 || ram.MediaCache != 98<<20 || ram.System != 28<<20 || ram.Free != 612<<20 {
		t.Errorf("ram = %+v, want total %d, server %d, media cache %d, system %d, free %d", ram, 1024<<20, 286<<20, 98<<20, 28<<20, 612<<20)
	}
	// Disk: 20 GB, 12.4 GB free, 1,000 bytes of reports and logs; the rest
	// (chat store included) is system and data.
	if d := got.Disk; d.Total != 20_000_000_000 || d.ReportsLogs != 1000 || d.SystemData != 7_599_999_000 || d.Free != 12_400_000_000 {
		t.Errorf("disk = %+v, want total 20000000000, reports and logs 1000, system and data 7599999000, free 12400000000", d)
	}
	p := got.Process
	if !p.Running || p.Since == nil || !p.Since.Equal(time.Date(2026, 10, 22, 7, 58, 0, 0, time.UTC)) {
		t.Errorf("process running %v since %v, want running since 22 Oct 07:58", p.Running, p.Since)
	}
	if len(p.Load) != 3 || p.Load[0] != 0.42 || p.Load[1] != 0.38 || p.Load[2] != 0.31 {
		t.Errorf("load = %v, want 0.42 0.38 0.31", p.Load)
	}
	if p.FDs == nil || *p.FDs != 8 || p.Sockets == nil || *p.Sockets != 3 || p.Listen != ":5300" {
		t.Errorf("fds %v, sockets %v, listen %q; want 8, 3, :5300", p.FDs, p.Sockets, p.Listen)
	}
}

func TestStoppedServer(t *testing.T) {
	r := newRig(t)
	r.status = nil
	for p := range r.root {
		if strings.HasPrefix(p, "proc/4242/") {
			delete(r.root, p)
		}
	}
	r.sample()
	var got page
	r.getJSON("/api/server", &got)
	// Everything in use is the system's: 1,024 − 612 = 412 MiB.
	if ram := got.RAM; ram.Server != 0 || ram.MediaCache != 0 || ram.System != 412<<20 || ram.Free != 612<<20 {
		t.Errorf("ram = %+v, want nothing for thefeed-server, system %d, free %d", ram, 412<<20, 612<<20)
	}
	if p := got.Process; p.Running || p.Since != nil || p.FDs != nil || p.Sockets != nil || len(p.Load) != 3 {
		t.Errorf("process = %+v, want not running, no start time, descriptors or sockets, but the machine's load", p)
	}
}

func near(got *float64, want float64) bool { return got != nil && math.Abs(*got-want) < 1e-9 }

// points checks a chart's points: want maps an index to its value, every
// other point is null.
func points(t *testing.T, name string, got []*float64, want map[int]float64) {
	t.Helper()
	if len(got) != 60 {
		t.Fatalf("%s has %d points, want 60", name, len(got))
	}
	for i, p := range got {
		if w, ok := want[i]; ok && !near(p, w) || !ok && p != nil {
			t.Errorf("%s[%d] = %v, want %v", name, i, ptr(p), w)
		}
	}
}

func TestChartsFromTheLastHour(t *testing.T) {
	r := newRig(t)
	r.sample() // 12:00:00
	// 12:00:10: 200 of 500 ticks busy = 40%; in 1,250,000 B × 8 / 10 s = 1 Mb/s, out 3,000,000 B = 2.4 Mb/s.
	r.step(10*time.Second, 200, 300, 1_250_000, 3_000_000)
	// 12:00:20: 20%; in 0, out 1.2 Mb/s.
	r.step(10*time.Second, 100, 400, 0, 1_500_000)
	// 12:01:10, 50 s later: 60%; in 2,500,000 B × 8 / 50 s = 0.4 Mb/s, out 0.
	r.step(50*time.Second, 300, 200, 2_500_000, 0)

	var h page
	r.getJSON("/api/server", &h) // 1h is the default
	// One-minute points; the last is the minute now is in, 12:01.
	if h.Range != "1h" || h.Step != 60 || !h.From.Equal(time.Date(2026, 11, 4, 11, 2, 0, 0, time.UTC)) {
		t.Errorf("range %q, step %d, from %s; want 1h, 60, 11:02", h.Range, h.Step, h.From)
	}
	// 12:00 holds 40% and 20%, 12:01 holds 60%.
	points(t, "1h cpu", h.CPU.Points, map[int]float64{58: 30, 59: 60})
	if !near(h.CPU.Now, 60) || !near(h.CPU.Avg, 45) || !near(h.CPU.Peak, 60) {
		t.Errorf("1h cpu now %v, avg %v, peak %v; want 60, 45, 60", ptr(h.CPU.Now), ptr(h.CPU.Avg), ptr(h.CPU.Peak))
	}
	points(t, "1h out", h.Net.Out, map[int]float64{58: 1.8, 59: 0})
	points(t, "1h in", h.Net.In, map[int]float64{58: 0.5, 59: 0.4})
	// The scale is the next of 1, 2, 5, 10, 20, … Mb/s at or above the highest bar.
	if !near(h.Net.OutNow, 0) || !near(h.Net.InNow, 0.4) || !near(h.Net.OutPeak, 1.8) || h.Net.Scale != 2 {
		t.Errorf("1h net out now %v, in now %v, out peak %v, scale %v; want 0, 0.4, 1.8, 2", ptr(h.Net.OutNow), ptr(h.Net.InNow), ptr(h.Net.OutPeak), h.Net.Scale)
	}

	var q page
	r.getJSON("/api/server?range=15m", &q)
	// 15-second points from 11:46:15: 12:00:10 is in the 56th, 12:00:20 in
	// the 57th, 12:01:10 in the 60th.
	if q.Range != "15m" || q.Step != 15 || !q.From.Equal(time.Date(2026, 11, 4, 11, 46, 15, 0, time.UTC)) {
		t.Errorf("range %q, step %d, from %s; want 15m, 15, 11:46:15", q.Range, q.Step, q.From)
	}
	points(t, "15m cpu", q.CPU.Points, map[int]float64{55: 40, 56: 20, 59: 60})
	if !near(q.CPU.Avg, 40) || !near(q.CPU.Peak, 60) || !near(q.Net.OutPeak, 2.4) || q.Net.Scale != 5 {
		t.Errorf("15m cpu avg %v, peak %v, out peak %v, scale %v; want 40, 60, 2.4, 5", ptr(q.CPU.Avg), ptr(q.CPU.Peak), ptr(q.Net.OutPeak), q.Net.Scale)
	}
}

// threeSamples is TestChartsFromTheLastHour's samples: minute 12:00 averages
// 30% CPU, out 1.8 and in 0.5 Mb/s, minute 12:01 60%, out 0 and in 0.4.
func (r *rig) threeSamples() {
	r.sample() // 12:00:00
	r.step(10*time.Second, 200, 300, 1_250_000, 3_000_000)
	r.step(10*time.Second, 100, 400, 0, 1_500_000)
	r.step(50*time.Second, 300, 200, 2_500_000, 0)
}

func TestLongRangesFromStoredMinutes(t *testing.T) {
	r := newRig(t)
	r.threeSamples()
	// The per-minute averages outlive the panel.
	r.app.Close()
	r.start()

	// 24h: 24-minute points from yesterday 12:24; both minutes are in the
	// last, 12:00 to 12:24, as the average of the two minutes.
	var d page
	r.getJSON("/api/server?range=24h", &d)
	if d.Range != "24h" || d.Step != 24*60 || !d.From.Equal(time.Date(2026, 11, 3, 12, 24, 0, 0, time.UTC)) {
		t.Errorf("range %q, step %d, from %s; want 24h, 1440, 3 Nov 12:24", d.Range, d.Step, d.From)
	}
	points(t, "24h cpu", d.CPU.Points, map[int]float64{59: 45})
	points(t, "24h out", d.Net.Out, map[int]float64{59: 0.9})
	points(t, "24h in", d.Net.In, map[int]float64{59: 0.45})

	// 7d: 168-minute points, on whole weeks from Monday 00:00 UTC; the last
	// runs from Wednesday 10:48 to 13:36.
	var w page
	r.getJSON("/api/server?range=7d", &w)
	if w.Step != 168*60 || !w.From.Equal(time.Date(2026, 10, 28, 13, 36, 0, 0, time.UTC)) {
		t.Errorf("7d step %d, from %s; want 10080, 28 Oct 13:36", w.Step, w.From)
	}
	points(t, "7d cpu", w.CPU.Points, map[int]float64{59: 45})
	if !near(w.CPU.Avg, 45) || !near(w.CPU.Peak, 45) || !near(w.Net.OutPeak, 0.9) || w.Net.Scale != 1 {
		t.Errorf("7d cpu avg %v, peak %v, out peak %v, scale %v; want 45, 45, 0.9, 1", ptr(w.CPU.Avg), ptr(w.CPU.Peak), ptr(w.Net.OutPeak), w.Net.Scale)
	}
}

func TestMinutesAreKeptForAWeek(t *testing.T) {
	r := newRig(t)
	r.threeSamples() // minutes 4 Nov 12:00 and 12:01
	// A week and 20 seconds later, the first sample of minute 11 Nov 12:01
	// drops 4 Nov 12:00, which is now over a week old, and keeps 12:01.
	r.now = time.Date(2026, 11, 11, 12, 1, 10, 0, time.UTC)
	r.sample()
	r.step(10*time.Second, 100, 400, 0, 0)

	r.app.Close()
	db, err := bolt.Open(filepath.Join(r.adminDir, "admin.db"), 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := 0
	db.View(func(tx *bolt.Tx) error { n = tx.Bucket([]byte("sysmetrics.minutes")).Stats().KeyN; return nil })
	if n != 2 {
		t.Errorf("%d minutes stored, want 2: 4 Nov 12:01 and 11 Nov 12:01", n)
	}
}

func TestTodaysTrafficStartsAtLocalMidnight(t *testing.T) {
	r := newRig(t)
	tehran := time.FixedZone("Tehran", 3*3600+1800)
	r.now = time.Date(2026, 11, 4, 23, 59, 40, 0, tehran)
	r.signIn() // the session from 12:00 UTC has gone idle
	r.sample()
	r.step(10*time.Second, 100, 400, 500_000, 1_000_000) // 23:59:50, yesterday by the time it's read
	r.step(20*time.Second, 100, 400, 700_000, 2_000_000) // 00:00:10
	r.step(10*time.Second, 100, 400, 0, 3_000_000)       // 00:00:20
	var got page
	r.getJSON("/api/server", &got)
	if got.Net.SentToday != 5_000_000 || got.Net.ReceivedToday != 700_000 {
		t.Errorf("today sent %d, received %d; want 5000000, 700000", got.Net.SentToday, got.Net.ReceivedToday)
	}
}

func TestCountersThatJumpStartOver(t *testing.T) {
	r := newRig(t)
	r.sample()                                     // 12:00:00
	r.step(10*time.Second, 200, 300, 0, 3_000_000) // 12:00:10: 40%
	// 12:00:20: eth0 was recreated and counts from zero again.
	r.rx, r.tx = 0, 0
	r.step(10*time.Second, 100, 400, 1000, 1000)
	r.step(10*time.Second, 100, 400, 0, 0) // 12:00:30: 20%
	// 12:00:40: the default route moved to wg0.
	r.root["proc/net/route"] = &fstest.MapFile{Data: []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n" +
		"wg0\t00000000\t00000000\t0001\t0\t0\t50\t00000000\n")}
	r.step(10*time.Second, 300, 200, 0, 0)
	var got page
	r.getJSON("/api/server?range=15m", &got)
	// 15-second points from 11:45:45: 12:00:10 is in the 58th, 12:00:20 would
	// be in the 59th, 12:00:30 and 12:00:40 in the 60th. Only the samples
	// whose counters carried on from the one before are there.
	points(t, "cpu", got.CPU.Points, map[int]float64{57: 40, 59: 20})
}

func TestPublicResolversReachingThisServer(t *testing.T) {
	r := newRig(t)
	var got page
	r.getJSON("/api/server", &got)
	if got.Reach.Resolvers != nil || got.Process.Redirect != "" {
		t.Errorf("before any probe: resolvers %+v, redirect %q; want null and none", got.Reach.Resolvers, got.Process.Redirect)
	}

	// 1.1.1.1 passes on thefeed-server's NXDOMAIN and 8.8.8.8 an empty
	// answer; 9.9.9.9 can't reach the server and says SERVFAIL, and
	// 208.67.222.222 doesn't answer at all.
	r.rcode = map[string]int{"1.1.1.1:53": dns.RcodeNameError, "8.8.8.8:53": dns.RcodeSuccess, "9.9.9.9:53": dns.RcodeServerFailure}
	r.probe()
	r.now = r.now.Add(time.Minute)
	got = page{}
	r.getJSON("/api/server", &got)
	res := got.Reach.Resolvers
	if res == nil || res.Answered != 2 || res.Asked != 4 || !res.At.Equal(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("resolvers = %+v, want 2 of 4 answered at 12:00", res)
	}
	// The server listens on 5300 and the resolvers reached it on 53.
	if got.Process.Redirect != "53/udp" {
		t.Errorf("redirect = %q, want 53/udp", got.Process.Redirect)
	}
	// Each resolver was asked once, for a fresh name under the feed domain,
	// so no cache can answer for the server.
	seen := map[string]bool{}
	for i, a := range r.asked {
		server, name, _ := strings.Cut(a, " ")
		label, ok := strings.CutSuffix(name, ".t.example.com.")
		if server != m4[i] || !ok || label == "" || strings.Contains(label, ".") || seen[label] {
			t.Errorf("asked %q, want %s for a fresh label under t.example.com.", a, m4[i])
		}
		seen[label] = true
	}
	if len(r.asked) != 4 {
		t.Errorf("asked %d times, want 4", len(r.asked))
	}

	// While thefeed-server doesn't answer, there's no domain to ask for, and
	// the last count stands.
	r.status = nil
	r.probe()
	if len(r.asked) != 4 {
		t.Errorf("asked %d times with the server down, want still 4", len(r.asked))
	}
	r.getJSON("/api/server", &got)
	if res := got.Reach.Resolvers; res == nil || res.Answered != 2 {
		t.Errorf("after a probe with the server down, resolvers = %+v, want the 2 of 4 from before", res)
	}
}

// m4 is the rig's resolvers.
var m4 = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53", "208.67.222.222:53"}

func TestUnknownRange(t *testing.T) {
	r := newRig(t)
	w := r.get("/api/server?range=2h")
	var body struct{ Errors []admin.FieldError }
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusBadRequest || len(body.Errors) != 1 || body.Errors[0].Field != "range" {
		t.Errorf("range=2h: %d %s, want 400 on the range field", w.Code, w.Body)
	}
}

func TestGitHubFromStatus(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		name  string
		relay *controlapi.RelayStatus
		want  string // "" for null, else ok and detail
	}{
		{"relay off", nil, ""},
		{"uploading", &controlapi.RelayStatus{Repo: "op/media"}, "true "},
		// Refusals are GitHub answering.
		{"token denied", &controlapi.RelayStatus{TokenDenied: true, FailStreak: 3, LastError: "403 Resource not accessible by personal access token"}, "true 403 Resource not accessible by personal access token"},
		{"repo full", &controlapi.RelayStatus{Quota403: true, FailStreak: 1, LastError: "repo above its size quota"}, "true repo above its size quota"},
		{"failing", &controlapi.RelayStatus{FailStreak: 2, LastError: "dial tcp 140.82.112.6:443: i/o timeout"}, "false dial tcp 140.82.112.6:443: i/o timeout"},
	} {
		r.status.GHRelay = c.relay
		var got page
		r.getJSON("/api/server", &got)
		g := ""
		if gh := got.Reach.GitHub; gh != nil {
			g = fmt.Sprint(gh.OK, " ", gh.Detail)
		}
		if g != c.want {
			t.Errorf("%s: github = %q, want %q", c.name, g, c.want)
		}
		// thefeed-server's /status doesn't report these yet (ticket 03).
		if string(got.Reach.Telegram) != "null" || string(got.Reach.Nitter) != "null" {
			t.Errorf("%s: telegram %s, nitter %s; want null until /status has them", c.name, got.Reach.Telegram, got.Reach.Nitter)
		}
	}
}

// ptr prints a nullable number.
func ptr(f *float64) any {
	if f == nil {
		return "null"
	}
	return *f
}

func (r *rig) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", origin+path, nil)
	req.AddCookie(r.cookie)
	w := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(w, req)
	return w
}

// getJSON fetches path, expecting 200, into v.
func (r *rig) getJSON(path string, v any) {
	r.t.Helper()
	w := r.get(path)
	if w.Code != 200 {
		r.t.Fatalf("GET %s = %d %s", path, w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		r.t.Fatalf("GET %s: %v in %s", path, err, w.Body)
	}
}

func TestNotAvailableWithoutProc(t *testing.T) {
	r := newRig(t)
	for p := range r.root {
		if strings.HasPrefix(p, "proc/") {
			delete(r.root, p)
		}
	}
	r.sample()
	for _, path := range []string{"/api/server", "/api/server/pinned"} {
		w := r.get(path)
		var body struct{ Errors []admin.FieldError }
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusServiceUnavailable || len(body.Errors) != 1 || !strings.Contains(body.Errors[0].Message, "not available") {
			t.Errorf("GET %s = %d %s, want 503 saying not available", path, w.Code, w.Body)
		}
	}
}
