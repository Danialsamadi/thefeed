package server

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

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
		st.MTProto = &controlapi.MTProto{Sessions: sessions, LiveTunnels: d.mtproto.LiveTunnels(), MaxTunnels: max}
	}
	return st
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
