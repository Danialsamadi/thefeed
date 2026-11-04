package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/sartoopjj/thefeed/internal/controlapi"
	"github.com/sartoopjj/thefeed/internal/protocol"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
	"github.com/sartoopjj/thefeed/internal/version"
)

// control is the local control endpoint the admin panel talks to. The wire
// format is internal/controlapi's.
type control struct {
	dns       *DNSServer
	started   time.Time
	effective map[string]string
}

func (c *control) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(c.status())
	})
	mux.HandleFunc("POST /channels/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := c.dns.reloadChannels(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[control] channels reloaded")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
		if _, err := c.dns.adminRefresh(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /checks/{feature}", func(w http.ResponseWriter, r *http.Request) {
		f := r.PathValue("feature")
		run, ok := c.checks()[f]
		if !ok {
			http.Error(w, fmt.Sprintf("no check for %q", f), http.StatusNotFound)
			return
		}
		if run == nil {
			http.Error(w, f+" is off", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(run(r.Context()))
	})
	mux.HandleFunc("POST /answering", func(w http.ResponseWriter, r *http.Request) {
		var req controlapi.Answering
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.On == nil {
			http.Error(w, `want {"on": true} or {"on": false}`, http.StatusBadRequest)
			return
		}
		c.dns.setAnswering(*req.On)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func (c *control) status() controlapi.Status {
	d := c.dns
	st := controlapi.Status{
		Server: controlapi.Server{
			Version:   version.Version,
			Started:   c.started,
			Answering: !d.paused.Load(),
			Config:    map[string]string{},
		},
		GHRelay: d.feed.GitHubRelay().Status(),
	}
	for k, v := range c.effective {
		if s, ok := serverconfig.Lookup(k); ok && !s.Secret {
			st.Server.Config[k] = v
		}
	}
	st.Feed.Domains = d.counts.snapshot(d.zones(), time.Now())
	if d.chat != nil {
		ch := d.chat.controlStats(time.Now())
		st.Chat = &ch
	}
	if mc := d.feed.MediaCache(); mc != nil {
		s := mc.Stats()
		st.MediaCache = &s
	}
	if d.mtproto != nil {
		sessions, max := d.mtproto.Stats()
		st.MTProto = &controlapi.MTProto{Sessions: sessions, LiveTunnels: d.mtproto.LiveTunnels(), MaxTunnels: max, DCs: d.mtproto.dcStats(time.Now())}
	}
	if r, ok := d.channelCtl.(interface{ controlStats() *controlapi.Channels }); ok {
		st.Channels = r.controlStats()
	}
	if d.xReader != nil {
		st.X = d.xReader.controlStats()
	}
	return st
}

// probeTimeout bounds each probe a check sends, well inside the panel
// client's 30 seconds.
const probeTimeout = 10 * time.Second

// checks is each feature's check by the panel's feature id, nil for a
// feature that is off.
func (c *control) checks() map[string]func(context.Context) controlapi.CheckResult {
	d := c.dns
	m := map[string]func(context.Context) controlapi.CheckResult{"x": nil, "gh": nil, "tg": nil, "chat": nil}
	if d.xReader != nil {
		m["x"] = d.xReader.check
	}
	if g := d.feed.GitHubRelay(); g != nil {
		m["gh"] = g.check
	}
	if d.mtproto != nil {
		m["tg"] = d.mtproto.check
	}
	if d.chat != nil {
		m["chat"] = d.chat.store.check
	}
	return m
}

// controlStats is the reader's fetch cycles; nil for a nil reader, which
// NewDNSServer may hold.
func (tr *TelegramReader) controlStats() *controlapi.Channels {
	if tr == nil {
		return nil
	}
	tr.mu.RLock()
	defer tr.mu.RUnlock()
	return tr.cycles.channels(tr.fetchInterval)
}

// controlStats is the reader's fetch cycles.
func (pr *PublicReader) controlStats() *controlapi.Channels {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return pr.cycles.channels(pr.fetchInterval)
}

// controlStats is the reader's accounts and each instance's last result.
func (xr *XPublicReader) controlStats() *controlapi.X {
	xr.mu.RLock()
	defer xr.mu.RUnlock()
	st := &controlapi.X{Accounts: len(xr.accounts), LastPost: xr.lastPost}
	for _, in := range xr.instances {
		res, ok := xr.results[in]
		if !ok {
			res.Host = in
		}
		st.Instances = append(st.Instances, res)
	}
	return st
}

// cycleLog is a channel reader's last fetch cycles.
type cycleLog struct {
	mu     sync.Mutex
	cycles []controlapi.FetchCycle // oldest first
}

func (l *cycleLog) add(c controlapi.FetchCycle) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cycles = append(l.cycles, c)
	if n := len(l.cycles); n > 10 {
		l.cycles = l.cycles[n-10:]
	}
}

// channels is the log for status, with the reader's interval every.
func (l *cycleLog) channels(every time.Duration) *controlapi.Channels {
	l.mu.Lock()
	defer l.mu.Unlock()
	return &controlapi.Channels{IntervalMs: every.Milliseconds(), Cycles: append([]controlapi.FetchCycle{}, l.cycles...)}
}

var (
	chatCheckBucket = []byte("chat_check")
	// One lock for every store; there is one per process.
	chatCheckMu sync.Mutex // two checks at once would share the bucket
)

// check stores a test message, reads it back and deletes it, each in its
// own transaction, so it goes through the file like real messages do. It
// lives in a bucket of its own, deleted with it, so no account sees it.
func (s *ChatStore) check(context.Context) controlapi.CheckResult {
	chatCheckMu.Lock()
	defer chatCheckMu.Unlock()
	key, want := []byte("message"), fmt.Appendf(nil, "control check %d", time.Now().UnixNano())
	err := s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(chatCheckBucket)
		if err != nil {
			return err
		}
		return b.Put(key, want)
	})
	if err == nil {
		err = s.db.View(func(tx *bolt.Tx) error {
			if got := tx.Bucket(chatCheckBucket).Get(key); !bytes.Equal(got, want) {
				return fmt.Errorf("read back %q, want %q", got, want)
			}
			return nil
		})
	}
	err = errors.Join(err, s.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(chatCheckBucket) == nil {
			return nil
		}
		return tx.DeleteBucket(chatCheckBucket)
	}))
	res := controlapi.CheckResult{OK: true, Summary: "stored, read back and deleted a test message."}
	if s.failed(err) != nil {
		res = controlapi.CheckResult{Summary: "the message store failed.", Detail: err.Error()}
	}
	log.Printf("[chat] check: %s %s", res.Summary, res.Detail)
	return res
}

// dcIDs is every data centre the relay dials, in order.
func (r *MTProtoRelay) dcIDs() []uint8 {
	ids := make([]uint8, 0, len(r.addr))
	for id := range r.addr {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// dcStats is each data centre's dials, for status.
func (r *MTProtoRelay) dcStats(now time.Time) []controlapi.DC {
	r.dialMu.Lock()
	defer r.dialMu.Unlock()
	var out []controlapi.DC
	for _, id := range r.dcIDs() {
		dc := controlapi.DC{ID: int(id)}
		if d := r.dials[id]; d != nil {
			dc = d.last
			dc.FailuresLastHour = int(d.fails.sum(now))
		}
		out = append(out, dc)
	}
	return out
}

// check dials every data centre at once and hangs up.
func (r *MTProtoRelay) check(ctx context.Context) controlapi.CheckResult {
	ctx, cancel := context.WithTimeout(ctx, min(probeTimeout, r.cfg.DialTimeout))
	defer cancel()
	ids := r.dcIDs()
	detail := make([]string, len(ids))
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			start := time.Now()
			conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", r.addr[id])
			took := time.Since(start)
			r.noteDial(id, took, err)
			if err != nil {
				detail[i] = fmt.Sprintf("dc%d %v", id, err)
				return
			}
			conn.Close()
			ok.Add(1)
			detail[i] = fmt.Sprintf("dc%d %dms", id, took.Milliseconds())
		})
	}
	wg.Wait()
	n := int(ok.Load())
	res := controlapi.CheckResult{OK: n == len(ids), Summary: answered(n, len(ids), "data centre", "data centres"), Detail: strings.Join(detail, " ")}
	log.Printf("[mtproto] check: %s %s", res.Summary, res.Detail)
	return res
}

// check is the recovery probe, run now whether or not the relay is parked:
// it reads the repo, keeps a token refusal, and resumes a parked relay
// whose repo is usable again.
func (g *GitHubRelay) check(ctx context.Context) controlapi.CheckResult {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	err := g.RefreshRepoSize(ctx)
	var res controlapi.CheckResult
	g.mu.Lock()
	repo := g.cfg.Repo
	switch {
	case isTokenAccessDenied(err):
		g.tokenDenied = true
		g.lastFlushErr = trimErrBody(err.Error())
		res.Summary = "GitHub refuses the token for " + repo + "."
	case err != nil:
		res.Summary = "can't read " + repo + "."
	case g.repoSizeKB >= g.repoQuotaKB:
		res.Summary = repo + " is over its size budget."
	default:
		res.OK = true
		res.Summary = fmt.Sprintf("%s answered, %d%% of its size budget used.", repo, g.repoSizeKB*100/g.repoQuotaKB)
		if g.quotaExhausted || g.tokenDenied {
			g.resumeLocked()
		}
	}
	g.mu.Unlock()
	if err != nil {
		res.Detail = trimErrBody(err.Error())
	}
	log.Printf("[gh-relay] check: %s %s", res.Summary, res.Detail)
	return res
}

// check probes every instance at once, with the first account.
func (xr *XPublicReader) check(ctx context.Context) controlapi.CheckResult {
	if len(xr.accounts) == 0 {
		return controlapi.CheckResult{Summary: "there are no X accounts to check the instances with."}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	errs := make([]error, len(xr.instances))
	var wg sync.WaitGroup
	for i, in := range xr.instances {
		wg.Go(func() { _, _, _, errs[i] = xr.tryInstance(ctx, in, xr.accounts[0]) })
	}
	wg.Wait()
	ok, detail := 0, make([]string, len(errs))
	for i, err := range errs {
		if err == nil {
			ok++
			detail[i] = xr.instances[i] + " ok"
		} else {
			detail[i] = xr.instances[i] + " " + err.Error()
		}
	}
	res := controlapi.CheckResult{OK: ok == len(errs), Summary: answered(ok, len(errs), "instance", "instances"), Detail: strings.Join(detail, "; ")}
	log.Printf("[x] check: %s %s", res.Summary, res.Detail)
	return res
}

// answered is a check's summary: how many of n answered.
func answered(ok, n int, one, many string) string {
	switch {
	case ok == n && n == 1:
		return "the " + one + " answered."
	case ok == n:
		return fmt.Sprintf("all %d %s answered.", n, many)
	case ok == 0:
		return "none of the " + many + " answered."
	}
	return fmt.Sprintf("%d of %d %s answered.", ok, n, many)
}

// setAnswering pauses or resumes DNS answers. Paused, the listener stays
// bound but no query gets any response, and the Telegram-over-DNS tunnels
// close. It isn't saved: a restart always answers.
func (s *DNSServer) setAnswering(on bool) {
	s.paused.Store(!on)
	if !on && s.mtproto != nil {
		s.mtproto.closeAll()
	}
	log.Printf("[control] answering %s", map[bool]string{true: "on", false: "off"}[on])
}

// chatToday is the messenger's counts for the current UTC day.
type chatToday struct {
	mu        sync.Mutex
	day       int64 // unix day the counts are for
	active    map[[protocol.AddressSize]byte]struct{}
	messages  int64
	inboxFull int64
}

// do runs f on now's counts, under the lock, starting a new day if needed.
func (d *chatToday) do(now time.Time, f func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if day := now.Unix() / 86400; day != d.day || d.active == nil {
		d.day, d.active, d.messages, d.inboxFull = day, map[[protocol.AddressSize]byte]struct{}{}, 0, 0
	}
	f()
}

func (c *ChatService) controlStats(now time.Time) controlapi.Chat {
	var st controlapi.Chat
	c.today.do(now, func() {
		st = controlapi.Chat{ActiveToday: len(c.today.active), MessagesToday: c.today.messages, InboxFullToday: c.today.inboxFull}
	})
	st.Waiting = c.store.Waiting()
	st.StoreErrors = c.store.recentErrors(now)
	return st
}

// window counts events over the last 60 steps, in one slot per step, so the
// sum slides with the clock instead of resetting. Not safe for concurrent
// use.
type window struct {
	step time.Duration
	n    [60]int64
	at   [60]int64 // the step number n[i] counts for
}

func (w *window) add(now time.Time) {
	t := now.UnixNano() / int64(w.step)
	i := t % 60
	if w.at[i] != t {
		w.at[i], w.n[i] = t, 0
	}
	w.n[i]++
}

func (w *window) sum(now time.Time) (total int64) {
	t := now.UnixNano() / int64(w.step)
	for i := range w.n {
		if t-w.at[i] < 60 {
			total += w.n[i]
		}
	}
	return total
}

// queryCounts is each domain's queries since start and in the last minute,
// fed by the same events as the hourly report.
type queryCounts struct {
	mu sync.Mutex
	m  map[string]*domainCount
}

type domainCount struct {
	total  int64
	minute window
}

func (q *queryCounts) record(ev reportEvent, now time.Time) {
	if ev.invalid || ev.domain == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.m == nil {
		q.m = map[string]*domainCount{}
	}
	dc := q.m[ev.domain]
	if dc == nil {
		dc = &domainCount{minute: window{step: time.Second}}
		q.m[ev.domain] = dc
	}
	dc.total++
	dc.minute.add(now)
}

func (q *queryCounts) snapshot(domains []string, now time.Time) []controlapi.DomainQueries {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]controlapi.DomainQueries, 0, len(domains))
	for _, d := range domains {
		dq := controlapi.DomainQueries{Domain: d}
		if dc := q.m[d]; dc != nil {
			dq.Queries, dq.PerMinute = dc.total, dc.minute.sum(now)
		}
		out = append(out, dq)
	}
	return out
}

// serveControl serves h on a Unix socket at path, mode 0600, until stop is
// called. Stop also removes the socket file.
func serveControl(path string, h http.Handler) (stop func(), err error) {
	// A crash leaves the file behind, and Listen won't bind over it.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	return func() { srv.Close() }, nil
}
