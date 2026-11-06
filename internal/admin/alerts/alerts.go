// Package alerts is Control, Alerts: rules on what the other modules
// publish (admin.Reading), and the places alerts go, as Shoutrrr URLs
// (Telegram, Discord, Slack, ntfy, email and more).
//
// Every minute each rule is held against the latest readings. One whose
// condition has held for its minutes sends once; it sends again when the
// condition clears. A reading older than 3 minutes neither fires nor
// clears anything, and while the server is stopped or not answering the
// feature rule waits: every feature is down then, and the server rule
// already says so. What has been sent is kept in the panel's database, so a
// restart of the panel doesn't send it twice. It counts as sent once one URL
// took it; until then it's tried again every minute.
//
// # API
//
//	GET  /api/alerts                 {rules, urls, firing}
//	POST /api/alerts/rules/{id}      {on, above, minutes}: the rule as saved
//	POST /api/alerts/urls            {url, password}: adds a place to send to
//	POST /api/alerts/urls/remove     {index}
//	POST /api/alerts/test            {results: [{url, error}]}, one test message to each
//
// A URL holds its service's token, so it's only ever shown as its scheme and
// host, and errors from sending are scrubbed of it.
package alerts

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// Sender sends a message to a Shoutrrr URL. Shoutrrr is the real one;
// tests fake it.
type Sender interface {
	Check(url string) error // whether url names a service it can send to
	Send(url, message string) error
}

// Options are the module's outside world.
type Options struct {
	Sender Sender
	// Every runs f now and then every d until the panel stops; nil uses a
	// ticker. Tests capture f to run it on their own clock.
	Every func(d time.Duration, f func())
}

// rule is one alert rule, as saved and as the API speaks it.
type rule struct {
	ID      string  `json:"id"`
	On      bool    `json:"on"`
	Above   float64 `json:"above"` // a percent for cpu, ram and disk, Mb/s for net; unused by server and feature
	Minutes int     `json:"minutes"`
}

// defaults are the rules before the operator changes them. A restart drops
// about 10 seconds, so the server's 2 minutes and the features' 5 pass a
// restart by quietly.
var defaults = []rule{
	{ID: "server", On: true, Minutes: 2},
	{ID: "feature", On: true, Minutes: 5},
	{ID: "cpu", On: true, Above: 90, Minutes: 10},
	{ID: "ram", On: true, Above: 90, Minutes: 10},
	{ID: "disk", On: true, Above: 90, Minutes: 5},
	{ID: "net", On: false, Above: 100, Minutes: 10},
}

// maxAbove is the highest threshold each measured rule takes.
var maxAbove = map[string]float64{"cpu": 100, "ram": 100, "disk": 100, "net": 100000}

// stale is how old a reading may be and still count.
const stale = 3 * time.Minute

// firing is an alert that was sent and hasn't cleared.
type firing struct {
	Key   string    `json:"key"`
	Since time.Time `json:"since"` // when its condition began to hold
	Text  string    `json:"text"`  // what was sent
}

type mod struct {
	d    *admin.Deps
	send Sender
	db   *admin.Bucket

	mu     sync.Mutex
	since  map[string]time.Time // when each reading's condition began to hold
	firing map[string]firing    // also under "firing" in db
}

// New is the alerts module.
func New(o Options) admin.Module {
	return func(d *admin.Deps) error {
		db, err := d.Bucket("alerts")
		if err != nil {
			return err
		}
		m := &mod{d: d, send: o.Sender, db: db, since: map[string]time.Time{}, firing: map[string]firing{}}
		var saved []firing
		if _, err := db.Get("firing", &saved); err != nil {
			return err
		}
		for _, f := range saved {
			m.firing[f.Key] = f
		}
		d.Handle("GET /api/alerts", m.handleGet)
		d.Handle("POST /api/alerts/rules/{id}", m.handleRule)
		d.Handle("POST /api/alerts/urls", m.handleAdd)
		d.Handle("POST /api/alerts/urls/remove", m.handleRemove)
		d.Handle("POST /api/alerts/test", m.handleTest)
		every := o.Every
		if every == nil {
			every = func(p time.Duration, f func()) {
				go func() {
					t := time.NewTicker(p)
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
			}
		}
		every(time.Minute, m.tick)
		return nil
	}
}

// Module is the alerts module sending through Shoutrrr.
var Module = New(Options{Sender: shoutrrrSender{}})

// rules are the saved rules, with the defaults for any not saved.
func (m *mod) rules() []rule {
	var saved []rule
	m.db.Get("rules", &saved)
	out := slices.Clone(defaults)
	for i, r := range out {
		if j := slices.IndexFunc(saved, func(s rule) bool { return s.ID == r.ID }); j >= 0 {
			out[i] = saved[j]
		}
	}
	return out
}

func (m *mod) urls() []string {
	var u []string
	m.db.Get("urls", &u)
	return u
}

// ruleOf is the rule that watches reading key: feature.x is the feature
// rule's, net.out the net rule's.
func ruleOf(key string, rules []rule) (rule, bool) {
	id, _, _ := strings.Cut(key, ".")
	i := slices.IndexFunc(rules, func(r rule) bool { return r.ID == id })
	if i < 0 {
		return rule{}, false
	}
	return rules[i], true
}

func holds(r rule, rd admin.Reading) bool {
	switch r.ID {
	case "server":
		return rd.State != "running"
	case "feature":
		return rd.State == "down"
	}
	return rd.Value > r.Above
}

// tick holds every rule against the latest readings, and sends what changed.
func (m *mod) tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.d.Clock.Now()
	rules, urls, rs := m.rules(), m.urls(), m.d.Readings()
	srv, ok := rs["server"]
	serverOut := ok && now.Sub(srv.At) < stale && srv.State != "running"
	keys := make([]string, 0, len(rs))
	for k := range rs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	changed := false
	for _, key := range keys {
		rd := rs[key]
		r, ok := ruleOf(key, rules)
		if !ok || now.Sub(rd.At) >= stale || r.ID == "feature" && serverOut {
			continue
		}
		if !r.On {
			delete(m.since, key)
			if _, sent := m.firing[key]; sent {
				delete(m.firing, key)
				changed = true
			}
			continue
		}
		f, sent := m.firing[key]
		if holds(r, rd) {
			if _, ok := m.since[key]; !ok {
				m.since[key] = now
			}
			if !sent && now.Sub(m.since[key]) >= time.Duration(r.Minutes)*time.Minute {
				text := m.fired(r, rd, m.since[key])
				if m.deliver(urls, text) {
					m.firing[key] = firing{Key: key, Since: m.since[key], Text: text}
					changed = true
				}
			}
			continue
		}
		delete(m.since, key)
		if sent && m.deliver(urls, m.cleared(r, rd, f.Since, now)) {
			delete(m.firing, key)
			changed = true
		}
	}
	if changed {
		m.save()
	}
}

func (m *mod) save() {
	out := make([]firing, 0, len(m.firing))
	for _, f := range m.firing {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	if err := m.db.Put("firing", out); err != nil {
		log.Printf("alerts: %v", err)
	}
}

// deliver sends text to every URL, and is true when one of them took it.
func (m *mod) deliver(urls []string, text string) bool {
	ok := false
	for _, u := range urls {
		if err := m.send.Send(u, text); err != nil {
			log.Printf("alerts: sending to %s: %s", masked(u), scrub(err, u))
			continue
		}
		ok = true
	}
	return ok
}

// where names the server in a message: its feed domain.
func (m *mod) where() string {
	data, _, _ := serverconfig.ReadFile(admin.EnvFile(m.d.ServerDataDir))
	if d, _ := serverconfig.ParseEnv(data).Get("THEFEED_DOMAIN"); d != "" {
		return d
	}
	return "the server"
}

func at(t time.Time) string { return t.Format("15:04 MST") }

func minutes(d time.Duration) string {
	n := max(1, int(math.Round(d.Minutes())))
	if n == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", n)
}

func (m *mod) fired(r rule, rd admin.Reading, since time.Time) string {
	w := m.where()
	switch r.ID {
	case "server":
		return fmt.Sprintf("thefeed-server on %s is %s, since %s.", w, rd.State, at(since))
	case "feature":
		return fmt.Sprintf("%s is down on %s, since %s.", rd.Name, w, at(since))
	}
	return fmt.Sprintf("%s on %s is %s, above %s for %d minutes.", rd.Name, w, value(r.ID, rd.Value), value(r.ID, r.Above), r.Minutes)
}

func (m *mod) cleared(r rule, rd admin.Reading, since, now time.Time) string {
	w := m.where()
	switch r.ID {
	case "server":
		return fmt.Sprintf("thefeed-server on %s is running again, after %s.", w, minutes(now.Sub(since)))
	case "feature":
		return fmt.Sprintf("%s works again on %s, after %s.", rd.Name, w, minutes(now.Sub(since)))
	}
	return fmt.Sprintf("%s on %s is back to %s.", rd.Name, w, value(r.ID, rd.Value))
}

// value says a reading or threshold in its rule's unit.
func value(id string, v float64) string {
	switch id {
	case "net":
		return fmt.Sprintf("%.3g Mb/s", v)
	case "ram":
		return fmt.Sprintf("%.0f%% used", v)
	case "disk":
		return fmt.Sprintf("%.0f%% full", v)
	}
	return fmt.Sprintf("%.0f%%", v)
}

// masked is a URL as the panel shows it: its scheme and host, never the
// token in it.
func masked(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "(a URL that doesn't parse)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// scrub is err's message without anything secret from raw in it: the URL,
// its user and password, path parts and query values, as written and
// escaped. Senders put the token in their errors, such as Telegram's API URL.
func scrub(err error, raw string) string {
	msg := err.Error()
	parts := []string{raw}
	if u, e := url.Parse(raw); e == nil {
		if u.User != nil {
			parts = append(parts, u.User.String(), u.User.Username())
			if p, ok := u.User.Password(); ok {
				parts = append(parts, p)
			}
		}
		parts = append(parts, strings.Split(u.Path, "/")...)
		for _, vs := range u.Query() {
			parts = append(parts, vs...)
		}
	}
	for _, p := range slices.Clone(parts) {
		parts = append(parts, url.PathEscape(p), url.QueryEscape(p))
	}
	sort.Slice(parts, func(i, j int) bool { return len(parts[i]) > len(parts[j]) })
	for _, p := range parts {
		if len(p) >= 6 {
			msg = strings.ReplaceAll(msg, p, "…")
		}
	}
	return msg
}

// --- API ---

type urlOut struct {
	URL string `json:"url"` // masked
}

type state struct {
	Rules  []rule   `json:"rules"`
	URLs   []urlOut `json:"urls"`
	Firing []firing `json:"firing"`
	Where  string   `json:"where"`
}

func (m *mod) handleGet(w http.ResponseWriter, r *http.Request) {
	m.reply(w)
}

func (m *mod) reply(w http.ResponseWriter) {
	m.mu.Lock()
	s := state{Rules: m.rules(), URLs: []urlOut{}, Firing: []firing{}, Where: m.where()}
	for _, u := range m.urls() {
		s.URLs = append(s.URLs, urlOut{masked(u)})
	}
	for _, f := range m.firing {
		s.Firing = append(s.Firing, f)
	}
	m.mu.Unlock()
	sort.Slice(s.Firing, func(i, j int) bool { return s.Firing[i].Since.Before(s.Firing[j].Since) })
	admin.WriteJSON(w, http.StatusOK, s)
}

func (m *mod) handleRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in rule
	if !admin.ReadJSON(w, r, &in) {
		return
	}
	in.ID = id
	switch {
	case !slices.ContainsFunc(defaults, func(d rule) bool { return d.ID == id }):
		admin.WriteError(w, http.StatusNotFound, admin.FieldError{Message: "There's no such rule."})
		return
	case in.Minutes < 1 || in.Minutes > 1440:
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "minutes", Message: "Between 1 and 1440 minutes."})
		return
	case maxAbove[id] > 0 && (in.Above <= 0 || in.Above > maxAbove[id]):
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "above", Message: fmt.Sprintf("More than 0, up to %g.", maxAbove[id])})
		return
	}
	if maxAbove[id] == 0 {
		in.Above = 0
	}
	m.mu.Lock()
	rules := m.rules()
	rules[slices.IndexFunc(rules, func(x rule) bool { return x.ID == id })] = in
	err := m.db.Put("rules", rules)
	m.mu.Unlock()
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't save the rule."})
		return
	}
	m.reply(w)
}

// handleAdd adds a URL to send alerts to. It holds a token, so it takes
// the password again.
func (m *mod) handleAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL      string `json:"url"`
		Password string `json:"password"`
	}
	if !admin.ReadJSON(w, r, &in) {
		return
	}
	raw := strings.TrimSpace(in.URL)
	if err := m.send.Check(raw); err != nil {
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "url", Message: "That isn't a URL Shoutrrr can send to: " + scrub(err, raw)})
		return
	}
	if !m.d.CheckPassword(w, r, in.Password) {
		return
	}
	m.mu.Lock()
	urls := m.urls()
	err := error(nil)
	if !slices.Contains(urls, raw) {
		err = m.db.Put("urls", append(urls, raw))
	}
	m.mu.Unlock()
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't save the URL."})
		return
	}
	m.d.Record(admin.Action{Action: "Added a place to send alerts", Outcome: masked(raw)})
	m.reply(w)
}

func (m *mod) handleRemove(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Index int `json:"index"`
	}
	if !admin.ReadJSON(w, r, &in) {
		return
	}
	m.mu.Lock()
	urls := m.urls()
	if in.Index < 0 || in.Index >= len(urls) {
		m.mu.Unlock()
		admin.WriteError(w, http.StatusNotFound, admin.FieldError{Message: "That URL is already gone."})
		return
	}
	gone := urls[in.Index]
	err := m.db.Put("urls", slices.Delete(urls, in.Index, in.Index+1))
	m.mu.Unlock()
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't remove the URL."})
		return
	}
	m.d.Record(admin.Action{Action: "Removed a place to send alerts", Outcome: masked(gone)})
	m.reply(w)
}

// handleTest sends one test message to every URL, and says how each went.
func (m *mod) handleTest(w http.ResponseWriter, r *http.Request) {
	type result struct {
		URL   string `json:"url"`
		Error string `json:"error,omitempty"`
	}
	m.mu.Lock()
	urls := m.urls()
	m.mu.Unlock()
	if len(urls) == 0 {
		admin.WriteError(w, http.StatusConflict, admin.FieldError{Message: "Add a URL to send alerts to first."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	text := fmt.Sprintf("A test alert from thefeed's panel on %s. Alerts arrive here.", m.where())
	out := []result{}
	for _, u := range urls {
		res := result{URL: masked(u)}
		if ctx.Err() != nil {
			res.Error = "Not tried: the test ran out of time."
		} else if err := m.send.Send(u, text); err != nil {
			res.Error = scrub(err, u)
		}
		out = append(out, res)
	}
	admin.WriteJSON(w, http.StatusOK, map[string]any{"results": out})
}
