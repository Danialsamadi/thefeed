// Package sysmetrics is the panel's Server page: the machine's CPU, RAM,
// disk and network from /proc and statfs, who can reach whom, and
// thefeed-server's process facts. Linux only: elsewhere both routes answer
// 503 with "not available".
//
// CPU and network are sampled every 10 seconds and kept in memory for an
// hour; per-minute averages are stored in the panel's database for 7 days.
// Every 15 minutes the public resolvers (THEFEED_ADMIN_RESOLVERS, comma
// separated, default about 10 well-known ones) are asked for a fresh name
// under the feed domain.
//
// # API
//
//	GET /api/server?range=15m|1h|24h|7d   (default 1h)
//
//	{"range":"1h","from":"<time>","step":60,
//	 "cpu":{"now","avg","peak","points":[60]},
//	 "net":{"outNow","inNow","outPeak","scale","out":[60],"in":[60],"sentToday","receivedToday"},
//	 "ram":{"total","server","mediaCache","system","free"},
//	 "disk":{"total","reportsLogs","systemData","free"},
//	 "reach":{"resolvers":{"answered","asked","at"},"telegram":{"reached","of","untried"},"nitter":{…},"github":{"ok","detail"}},
//	 "process":{"running","since","load":[3],"fds","sockets","listen","redirect"}}
//
// Every chart has 60 points of step seconds from from; a point is the
// average of its samples, null without one. CPU is percent; network is Mb/s
// (10^6 bits) on the default-route interface, and scale is the chart's top
// (1, 2, 5, 10, … Mb/s). avg and peak are of the points, now is the latest
// sample; all three are null with no sample yet. Sizes are bytes; today's
// traffic counts from midnight in the panel's time zone. RAM's parts and
// disk's parts each add up to their total. The disk has no media cache
// part: thefeed-server keeps its media cache in memory.
//
// reach.resolvers is null before the first probe. telegram counts the data
// centres and nitter the instances whose last dial or fetch worked, of all
// of them, and how many weren't tried yet; each is null with its feature
// off, and github with the relay off. process.redirect is "53/udp" when the server listens on
// another port and public resolvers still reach it, else "".
//
//	GET /api/server/pinned   the Board's pinned line
//
//	{"cpu":40,"out":2.4,"ramUsed":432013312,"diskUsed":7600000000}
//
// cpu (percent) and out (Mb/s) are the latest sample, null before one;
// ramUsed and diskUsed are bytes.
package sysmetrics

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	bolt "go.etcd.io/bbolt"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/controlapi"
)

// Machine is everything the module reads from outside thefeed-admin.
type Machine struct {
	Root     fs.FS                                                                  // the filesystem from "/": proc/..., the data directory, var/log/journal
	Statfs   func(path string) (total, free uint64, err error)                      // size and space free to users of path's filesystem, in bytes
	Exchange func(ctx context.Context, q *dns.Msg, server string) (*dns.Msg, error) // one DNS query to server ("host:port")
	Every    func(every time.Duration, f func())                                    // runs f now and then every interval until the app closes
	Server   controlapi.Client
	// Resolvers are the public resolvers ("host:port") asked whether they
	// reach this server.
	Resolvers []string
}

// defaultResolvers are well-known public resolvers: Google, Cloudflare,
// Quad9, OpenDNS, AdGuard and Yandex.
var defaultResolvers = "8.8.8.8,8.8.4.4,1.1.1.1,1.0.0.1,9.9.9.9,149.112.112.112,208.67.222.222,208.67.220.220,94.140.14.14,77.88.8.8"

// Module is the sysmetrics module on this machine.
func Module(d *admin.Deps) error {
	list := os.Getenv("THEFEED_ADMIN_RESOLVERS")
	if strings.TrimSpace(list) == "" {
		list = defaultResolvers
	}
	var resolvers []string
	for _, r := range strings.FieldsFunc(list, func(c rune) bool { return c == ',' || c == ' ' }) {
		if _, _, err := net.SplitHostPort(r); err != nil {
			r = net.JoinHostPort(r, "53")
		}
		resolvers = append(resolvers, r)
	}
	return New(Machine{
		Root:   os.DirFS("/"),
		Statfs: statfs,
		Exchange: func(ctx context.Context, q *dns.Msg, server string) (*dns.Msg, error) {
			// The context can only shorten the client's own 2-second default.
			a, _, err := (&dns.Client{Timeout: 5 * time.Second}).ExchangeContext(ctx, q, server)
			return a, err
		},
		Every: func(every time.Duration, f func()) {
			go func() {
				t := time.NewTicker(every)
				defer t.Stop()
				for {
					f()
					select {
					case <-d.Ctx.Done():
						return
					case <-t.C:
					}
				}
			}()
		},
		Server:    d.Control(),
		Resolvers: resolvers,
	})(d)
}

// New is the sysmetrics module on m.
func New(m Machine) admin.Module {
	return func(d *admin.Deps) error {
		minutes, err := d.Bucket("sysmetrics.minutes")
		if err != nil {
			return err
		}
		dataDir, err := filepath.Abs(d.ServerDataDir)
		if err != nil {
			return err
		}
		s := &module{m: m, ctx: d.Ctx, clock: d.Clock, dataDir: dataDir, minutes: minutes}
		m.Every(10*time.Second, s.sample)
		m.Every(15*time.Minute, s.probe)
		d.Handle("GET /api/server", s.available(s.page))
		d.Handle("GET /api/server/pinned", s.available(s.pinned))
		return nil
	}
}

type module struct {
	m       Machine
	ctx     context.Context
	clock   admin.Clock
	dataDir string
	minutes *admin.Bucket

	lastMinute string // the minute store last wrote; only the sampler touches it

	mu      sync.Mutex
	last    *counters // the reading before, nil when there's nothing to diff against
	samples []sample  // the last hour, oldest first
	reached *resolvers
}

// resolvers is the last probe of the public resolvers.
type resolvers struct {
	Answered int       `json:"answered"`
	Asked    int       `json:"asked"`
	At       time.Time `json:"at"`
}

// probe asks each public resolver for a fresh name under the feed domain.
// A resolver can only answer it by asking thefeed-server, which says
// NXDOMAIN; one that can't reach the server says SERVFAIL or nothing.
//
// A broken delegation can get the parent zone's NXDOMAIN too;
// requiring no SOA in the answer (thefeed-server sends none) is the upgrade.
func (s *module) probe() {
	ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
	st, err := s.m.Server.Status(ctx)
	cancel()
	if err != nil || st.Server.Config["dns.domain"] == "" {
		return
	}
	res := resolvers{Asked: len(s.m.Resolvers), At: s.clock.Now()}
	for _, server := range s.m.Resolvers {
		q := new(dns.Msg)
		q.SetQuestion(dns.Fqdn(strings.ToLower(rand.Text())+"."+st.Server.Config["dns.domain"]), dns.TypeA)
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		a, err := s.m.Exchange(ctx, q, server)
		cancel()
		if err == nil && (a.Rcode == dns.RcodeNameError || a.Rcode == dns.RcodeSuccess) {
			res.Answered++
		}
	}
	s.mu.Lock()
	s.reached = &res
	s.mu.Unlock()
}

// sample is one 10-second step.
type sample struct {
	At      time.Time
	CPU     float64 // percent busy
	In, Out float64 // Mb/s on the default-route interface
	RX, TX  uint64  // bytes received and sent since the sample before
}

// counters are the raw totals a sample is the difference of.
type counters struct {
	at          time.Time
	busy, total uint64 // CPU ticks
	iface       string
	rx, tx      uint64
}

// sample reads the counters and keeps the step since the last reading.
func (s *module) sample() {
	c, _ := s.counters()
	s.mu.Lock()
	last := s.last
	s.last = c
	// Start over when a counter went backwards (an interface was recreated)
	// or the default route moved to another interface.
	if c == nil || last == nil || c.iface != last.iface || c.total <= last.total || c.rx < last.rx || c.tx < last.tx || !c.at.After(last.at) {
		s.mu.Unlock()
		return
	}
	secs := c.at.Sub(last.at).Seconds()
	busy := float64(int64(c.busy-last.busy)) / float64(c.total-last.total)
	smp := sample{At: c.at, CPU: 100 * math.Max(0, math.Min(1, busy)), RX: c.rx - last.rx, TX: c.tx - last.tx}
	smp.In = float64(smp.RX) * 8 / secs / 1e6
	smp.Out = float64(smp.TX) * 8 / secs / 1e6
	s.samples = append(s.samples, smp)
	for len(s.samples) > 0 && c.at.Sub(s.samples[0].At) > time.Hour {
		s.samples = s.samples[1:]
	}
	s.mu.Unlock()
	if err := s.store(smp); err != nil {
		log.Printf("sysmetrics: %v", err)
	}
}

// minute is one minute's samples, summed; stored for a week under its
// start in UTC.
type minute struct {
	N            int
	CPU, In, Out float64
	RX, TX       uint64
}

const minuteKey = "2006-01-02T15:04Z"

// store adds smp to its minute and, on a new minute, drops the minutes
// older than a week.
func (s *module) store(smp sample) error {
	key := smp.At.UTC().Format(minuteKey)
	var m minute
	if _, err := s.minutes.Get(key, &m); err != nil {
		return err
	}
	m.N, m.CPU, m.In, m.Out, m.RX, m.TX = m.N+1, m.CPU+smp.CPU, m.In+smp.In, m.Out+smp.Out, m.RX+smp.RX, m.TX+smp.TX
	if err := s.minutes.Put(key, m); err != nil || key == s.lastMinute {
		return err
	}
	s.lastMinute = key
	cutoff := []byte(smp.At.Add(-7 * 24 * time.Hour).UTC().Format(minuteKey))
	return s.minutes.Update(func(b *bolt.Bucket) error {
		var old [][]byte
		c := b.Cursor()
		for k, _ := c.First(); k != nil && bytes.Compare(k, cutoff) < 0; k, _ = c.Next() {
			old = append(old, bytes.Clone(k))
		}
		for _, k := range old {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// stored is the per-minute averages since from, oldest first.
func (s *module) stored(from time.Time) []sample {
	var out []sample
	s.minutes.View(func(b *bolt.Bucket) error {
		c := b.Cursor()
		for k, v := c.Seek([]byte(from.UTC().Format(minuteKey))); k != nil; k, v = c.Next() {
			var m minute
			at, err := time.Parse(minuteKey, string(k))
			if err != nil || json.Unmarshal(v, &m) != nil || m.N == 0 {
				continue
			}
			n := float64(m.N)
			out = append(out, sample{At: at, CPU: m.CPU / n, In: m.In / n, Out: m.Out / n, RX: m.RX, TX: m.TX})
		}
		return nil
	})
	return out
}

func (s *module) counters() (*counters, error) {
	stat, err := fs.ReadFile(s.m.Root, "proc/stat")
	if err != nil {
		return nil, err
	}
	line, _, _ := bytes.Cut(stat, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 9 || f[0] != "cpu" {
		return nil, errors.New("proc/stat: no cpu line")
	}
	// user nice system idle iowait irq softirq steal; guest and guest_nice
	// that follow are already counted in user and nice.
	c := &counters{at: s.clock.Now()}
	for i, v := range f[1:9] {
		n, _ := strconv.ParseUint(v, 10, 64)
		c.total += n
		if i != 3 && i != 4 {
			c.busy += n
		}
	}
	c.iface = s.defaultRoute()
	c.rx, c.tx = s.netBytes(c.iface)
	return c, nil
}

// defaultRoute is the interface of the IPv4 default route with the lowest
// metric, or "" without one.
func (s *module) defaultRoute() string {
	raw, _ := fs.ReadFile(s.m.Root, "proc/net/route")
	iface, best := "", uint64(math.MaxUint64)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
			continue
		}
		if m, err := strconv.ParseUint(f[6], 10, 64); err == nil && m < best {
			iface, best = f[0], m
		}
	}
	return iface
}

// netBytes is iface's received and sent byte counters from /proc/net/dev.
func (s *module) netBytes(iface string) (rx, tx uint64) {
	raw, _ := fs.ReadFile(s.m.Root, "proc/net/dev")
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || iface == "" || strings.TrimSpace(name) != iface {
			continue
		}
		if f := strings.Fields(rest); len(f) >= 9 {
			rx, _ = strconv.ParseUint(f[0], 10, 64)
			tx, _ = strconv.ParseUint(f[8], 10, 64)
		}
	}
	return rx, tx
}

// meminfo is /proc/meminfo in bytes, by name.
func (s *module) meminfo() map[string]uint64 {
	raw, _ := fs.ReadFile(s.m.Root, "proc/meminfo")
	out := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		name, rest, _ := strings.Cut(sc.Text(), ":")
		n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
		out[name] = n * 1024
	}
	return out
}

// available answers 503 on a machine without /proc, such as macOS.
func (s *module) available(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(s.m.Root, "proc/stat"); err != nil {
			admin.WriteError(w, http.StatusServiceUnavailable, admin.FieldError{Message: "System metrics are not available: they need Linux's /proc."})
			return
		}
		h(w, r)
	}
}

// latest is the newest sample, or nil.
func (s *module) latest() *sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.samples) == 0 {
		return nil
	}
	smp := s.samples[len(s.samples)-1]
	return &smp
}

type ram struct {
	Total      uint64 `json:"total"`
	Server     uint64 `json:"server"` // thefeed-server's RSS less the media cache
	MediaCache uint64 `json:"mediaCache"`
	System     uint64 `json:"system"` // everything else in use
	Free       uint64 `json:"free"`   // MemAvailable
}

type disk struct {
	Total       uint64 `json:"total"`
	ReportsLogs uint64 `json:"reportsLogs"` // hourly reports and the systemd journal
	SystemData  uint64 `json:"systemData"`  // everything else in use, the data directory included
	Free        uint64 `json:"free"`
}

type process struct {
	Running  bool       `json:"running"` // the control socket answers
	Since    *time.Time `json:"since"`
	Load     []float64  `json:"load"` // 1, 5 and 15 minutes
	FDs      *int       `json:"fds"`  // open file descriptors, null without the process
	Sockets  *int       `json:"sockets"`
	Listen   string     `json:"listen"`
	Redirect string     `json:"redirect"`
}

type cpuChart struct {
	Now    *float64   `json:"now"`
	Avg    *float64   `json:"avg"` // of the points
	Peak   *float64   `json:"peak"`
	Points []*float64 `json:"points"` // percent, null where there's no sample
}

type netChart struct {
	OutNow  *float64   `json:"outNow"`
	InNow   *float64   `json:"inNow"`
	OutPeak *float64   `json:"outPeak"`
	Scale   float64    `json:"scale"` // the chart's top: 1, 2, 5, 10, 20, … Mb/s
	Out     []*float64 `json:"out"`
	In      []*float64 `json:"in"`
	// Bytes since midnight in the panel's time zone.
	SentToday     uint64 `json:"sentToday"`
	ReceivedToday uint64 `json:"receivedToday"`
}

type serverPage struct {
	Range   string    `json:"range"`
	From    time.Time `json:"from"` // where the first point starts
	Step    int       `json:"step"` // seconds per point
	CPU     cpuChart  `json:"cpu"`
	Net     netChart  `json:"net"`
	RAM     ram       `json:"ram"`
	Disk    disk      `json:"disk"`
	Reach   reach     `json:"reach"`
	Process process   `json:"process"`
}

type reach struct {
	Resolvers *resolvers `json:"resolvers"` // null before the first probe
	// Telegram data centres and Nitter instances reached from here, by
	// their last dial or fetch: null with the feature off.
	Telegram *outOf  `json:"telegram"`
	Nitter   *outOf  `json:"nitter"`
	GitHub   *github `json:"github"` // null with the GitHub relay off
}

type outOf struct {
	Reached int `json:"reached"` // the last try worked
	Of      int `json:"of"`
	Untried int `json:"untried"` // not tried yet, so neither reached nor not
}

// count tallies the last tries: ok, and at, zero when never tried.
func count[T any](items []T, try func(T) (ok bool, at time.Time)) *outOf {
	o := &outOf{Of: len(items)}
	for _, it := range items {
		switch ok, at := try(it); {
		case ok:
			o.Reached++
		case at.IsZero():
			o.Untried++
		}
	}
	return o
}

type github struct {
	OK     bool   `json:"ok"`     // GitHub answers, even if only to refuse
	Detail string `json:"detail"` // the relay's last error
}

// ranges are the charts' time ranges, 60 points each.
var ranges = map[string]time.Duration{"15m": 15 * time.Second, "1h": time.Minute, "24h": 24 * time.Minute, "7d": 168 * time.Minute}

const nPoints = 60

func (s *module) page(w http.ResponseWriter, r *http.Request) {
	p := serverPage{Range: r.URL.Query().Get("range")}
	if p.Range == "" {
		p.Range = "1h"
	}
	step, ok := ranges[p.Range]
	if !ok {
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "range", Message: "The range is 15m, 1h, 24h or 7d."})
		return
	}
	// Points sit on whole steps, so they don't shift between polls; the
	// last one holds now.
	end := s.clock.Now().Truncate(step).Add(step)
	p.From, p.Step = end.Add(-nPoints*step), int(step.Seconds())
	// The last hour is in memory at 10-second detail; longer is per minute.
	samples := s.history(p.From)
	if end.Sub(p.From) > time.Hour {
		samples = s.stored(p.From)
	}
	s.charts(&p, samples, step)
	now := s.clock.Now()
	for _, m := range s.stored(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())) {
		p.Net.SentToday, p.Net.ReceivedToday = p.Net.SentToday+m.TX, p.Net.ReceivedToday+m.RX
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	st, _ := s.m.Server.Status(ctx)
	cancel()

	mem := s.meminfo()
	pid := s.serverPID()
	rss := s.rss(pid)
	p.RAM = ram{Total: mem["MemTotal"], Free: min(mem["MemAvailable"], mem["MemTotal"])}
	if st != nil && st.MediaCache != nil {
		p.RAM.MediaCache = min(uint64(max(st.MediaCache.RAMBytes, 0)), rss)
	}
	p.RAM.Server = rss - p.RAM.MediaCache
	p.RAM.System = p.RAM.Total - p.RAM.Free - min(rss, p.RAM.Total-p.RAM.Free)

	if total, free, err := s.m.Statfs(s.dataDir); err == nil {
		p.Disk = disk{Total: total, Free: min(free, total)}
		p.Disk.ReportsLogs = min(s.reportsAndLogs(), total-p.Disk.Free)
		p.Disk.SystemData = total - p.Disk.Free - p.Disk.ReportsLogs
	}

	p.Process.Load = s.load()
	if pid != "" {
		fds, sockets := s.fds(pid)
		p.Process.FDs, p.Process.Sockets = &fds, &sockets
	}
	s.mu.Lock()
	p.Reach.Resolvers = s.reached
	s.mu.Unlock()
	if st != nil && st.MTProto != nil {
		p.Reach.Telegram = count(st.MTProto.DCs, func(dc controlapi.DC) (bool, time.Time) { return dc.OK, dc.At })
	}
	if st != nil && st.X != nil {
		p.Reach.Nitter = count(st.X.Instances, func(in controlapi.Instance) (bool, time.Time) { return in.OK, in.At })
	}
	if st != nil && st.GHRelay != nil {
		g := st.GHRelay
		p.Reach.GitHub = &github{OK: g.FailStreak == 0 || g.TokenDenied || g.Quota403, Detail: g.LastError}
	}
	if st != nil {
		p.Process.Running = true
		p.Process.Since = &st.Server.Started
		p.Process.Listen = st.Server.Config["server.listen"]
		// Inferred, not read from iptables. Public resolvers
		// only ever ask on 53, so if they reach a server listening
		// elsewhere, something forwards 53 to it.
		if _, port, _ := net.SplitHostPort(p.Process.Listen); port != "53" && p.Reach.Resolvers != nil && p.Reach.Resolvers.Answered > 0 {
			p.Process.Redirect = "53/udp"
		}
	}
	admin.WriteJSON(w, http.StatusOK, p)
}

// history is the samples since from, oldest first.
func (s *module) history(from time.Time) []sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sample
	for _, smp := range s.samples {
		if !smp.At.Before(from) {
			out = append(out, smp)
		}
	}
	return out
}

// charts averages samples into p's points and fills in now, average, peak
// and scale.
func (s *module) charts(p *serverPage, samples []sample, step time.Duration) {
	var sum, n [3][nPoints]float64
	for _, smp := range samples {
		i := int(smp.At.Sub(p.From) / step)
		if i < 0 || i >= nPoints {
			continue
		}
		for k, v := range [3]float64{smp.CPU, smp.Out, smp.In} {
			sum[k][i] += v
			n[k][i]++
		}
	}
	var pts [3][]*float64
	var avg, peak [3]*float64
	for k := range pts {
		pts[k] = make([]*float64, nPoints)
		total, count := 0.0, 0
		for i := range nPoints {
			if n[k][i] == 0 {
				continue
			}
			v := sum[k][i] / n[k][i]
			pts[k][i] = &v
			total, count = total+v, count+1
			if peak[k] == nil || v > *peak[k] {
				peak[k] = &v
			}
		}
		if count > 0 {
			a := total / float64(count)
			avg[k] = &a
		}
	}
	p.CPU = cpuChart{Avg: avg[0], Peak: peak[0], Points: pts[0]}
	p.Net = netChart{OutPeak: peak[1], Out: pts[1], In: pts[2], Scale: scale(max(deref(peak[1]), deref(peak[2])))}
	if smp := s.latest(); smp != nil {
		p.CPU.Now, p.Net.OutNow, p.Net.InNow = &smp.CPU, &smp.Out, &smp.In
	}
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// scale is the first of 1, 2, 5, 10, 20, 50, … at or above peak.
func scale(peak float64) float64 {
	for s := 1.0; ; s *= 10 {
		for _, m := range []float64{1, 2, 5} {
			if m*s >= peak {
				return m * s
			}
		}
	}
}

// serverPID finds thefeed-server in /proc, or "".
func (s *module) serverPID() string {
	ents, _ := fs.ReadDir(s.m.Root, "proc")
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		if comm, _ := fs.ReadFile(s.m.Root, "proc/"+e.Name()+"/comm"); strings.TrimSpace(string(comm)) == "thefeed-server" {
			return e.Name()
		}
	}
	return ""
}

// rss is the process's resident memory in bytes.
func (s *module) rss(pid string) uint64 {
	if pid == "" {
		return 0
	}
	raw, _ := fs.ReadFile(s.m.Root, "proc/"+pid+"/status")
	for l := range strings.Lines(string(raw)) {
		if v, ok := strings.CutPrefix(l, "VmRSS:"); ok {
			n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return n * 1024
		}
	}
	return 0
}

// fds counts the process's open file descriptors and how many are sockets.
func (s *module) fds(pid string) (fds, sockets int) {
	dir := "proc/" + pid + "/fd"
	ents, _ := fs.ReadDir(s.m.Root, dir)
	for _, e := range ents {
		if l, _ := fs.ReadLink(s.m.Root, dir+"/"+e.Name()); strings.HasPrefix(l, "socket:") {
			sockets++
		}
	}
	return len(ents), sockets
}

func (s *module) load() []float64 {
	raw, _ := fs.ReadFile(s.m.Root, "proc/loadavg")
	f, out := strings.Fields(string(raw)), []float64{}
	for i := 0; i < 3 && i < len(f); i++ {
		n, _ := strconv.ParseFloat(f[i], 64)
		out = append(out, n)
	}
	return out
}

// reportsAndLogs is the size of the hourly reports in the data directory
// and of the systemd journal.
//
// Assumes the journal sits on the data directory's filesystem,
// which a one-disk VPS does.
func (s *module) reportsAndLogs() uint64 {
	var n uint64
	dir := strings.TrimPrefix(path.Clean(filepath.ToSlash(s.dataDir)), "/")
	ents, _ := fs.ReadDir(s.m.Root, dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil && strings.HasPrefix(e.Name(), "dns_hourly.jsonl") && info.Mode().IsRegular() {
			n += uint64(info.Size())
		}
	}
	fs.WalkDir(s.m.Root, "var/log/journal", func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			n += uint64(info.Size())
		}
		return nil
	})
	return n
}

func (s *module) pinned(w http.ResponseWriter, r *http.Request) {
	var p struct {
		CPU      *float64 `json:"cpu"`
		Out      *float64 `json:"out"`
		RAMUsed  uint64   `json:"ramUsed"`
		DiskUsed uint64   `json:"diskUsed"`
	}
	if smp := s.latest(); smp != nil {
		p.CPU, p.Out = &smp.CPU, &smp.Out
	}
	mem := s.meminfo()
	p.RAMUsed = mem["MemTotal"] - min(mem["MemAvailable"], mem["MemTotal"])
	if total, free, err := s.m.Statfs(s.dataDir); err == nil {
		p.DiskUsed = total - min(free, total)
	}
	admin.WriteJSON(w, http.StatusOK, p)
}
