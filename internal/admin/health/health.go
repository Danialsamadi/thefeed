// Package health is the panel's view of how each of thefeed-server's
// features is doing: a sample of /status every minute, judged by the rules
// in features.go, kept as history, and served to the Board and the feature
// pages.
//
// Feature ids are the logs module's: x tg feed chan chat dnsm gh. Each has
// one of four states, ok, slow, down or off. While the server is stopped
// (its control socket doesn't answer) or not answering DNS, every feature is
// down, worded "Stopped" or "Not answering".
//
// # History
//
// A sample is stored per minute for 7 days. Each day rolls up to one state
// per feature, kept 90 days: off when off all day, down with 30 or more
// minutes down, slow with any slow or down minute, else up. A day without
// samples reads as off. Uptime is the days that weren't down of the days
// that weren't off. An incident opens on the second sample in a row that is
// slow or down, starting at the first, and closes on the second ok (or off)
// one, ending at the first; finished incidents are kept 90 days.
//
// # API
//
//	GET  /api/board                {server, attention, features}
//	GET  /api/features/{feature}   one feature's page
//	POST /api/checks/{feature}     run its check now
//
// The Board:
//
//	{"server":{"state":"running|not answering|stopped","since","version","lastSeen"},
//	 "attention":2,                       // features slow or down
//	 "features":[{"id","name","state","word","line","uptime"}]}
//
// uptime is {"days":[60 × up|slow|down|off, today last],"percent":61.7 or
// null when every day was off,"up","slow","down","off"}. word is Working,
// Slow, Down, Off, Stopped or Not answering; line is the row's one fact.
// The pinned CPU, RAM, disk and network figures come from the sysmetrics
// module's own API.
//
// A feature page adds:
//
//	{"since",                              // when the state began, as far back as samples go
//	 "impact",                             // what it means for readers
//	 "actions":[{"id":"check|edit|page","label","page"}],  // page: Control page id, or "svc"
//	 "facts":[{"label","value","state"}],  // state "", slow or down
//	 "deps":{"title","cols","rows":[[...]]} or null when off,  // a Status column holds ok|slow|down|pending
//	 "incidents":[{"start","end" or null while it lasts,"worst","summary"}],  // newest first
//	 "lastCheck","nextCheck",              // the automatic sample
//	 "checked":{"at","ok","pending","summary","detail"} or null}  // the last Check now
//
// A check answers 200 with the checked object. Telegram channels' check is
// Fetch now: it answers 202 with pending true, and the feature page's
// checked fills in when status has a cycle that started after it, or says
// none came back after 3 fetch intervals. feed sends
// the test query and dnsm samples again; the rest run the server's own
// check. 404 for an unknown feature, 409 for one that's off, 502 when the
// server couldn't run it.
package health

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/controlapi"
)

// SelfTest asks the server's own DNS listener, at listen, for the feed on
// domain the way a reader's app would, and says how long the answer took.
type SelfTest interface {
	Query(ctx context.Context, listen, domain string) (time.Duration, error)
}

// New is the health module reading the server through c, and testing the
// feed with dns.
func New(c controlapi.Client, dns SelfTest) admin.Module {
	return func(d *admin.Deps) error {
		db, err := d.Bucket("health")
		if err != nil {
			return err
		}
		h := &health{c: c, dns: dns, clock: d.Clock, db: db, checked: map[string]*checkResult{}}
		d.Handle("GET /api/board", h.handleBoard)
		d.Handle("GET /api/features/{feature}", h.handleFeature)
		d.Handle("POST /api/checks/{feature}", h.handleCheck)
		// Requests sample too, when a minute has no sample yet; this keeps
		// history going while nobody has the panel open.
		go func() {
			t := time.NewTicker(10 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-d.Ctx.Done():
					return
				case <-t.C:
					h.mu.Lock()
					h.fresh(d.Ctx)
					h.mu.Unlock()
				}
			}
		}()
		return nil
	}
}

type health struct {
	c     controlapi.Client
	dns   SelfTest
	clock admin.Clock
	db    *admin.Bucket

	mu       sync.Mutex // one sample at a time; guards everything below
	at       time.Time  // the last sample
	st       *controlapi.Status
	server   string // running, not answering, stopped
	lastSeen time.Time
	tests    map[string]test // the feed self-test, by domain
	views    map[string]view
	since    map[string]time.Time // when each feature's state, and the server's ("server"), began
	checked  map[string]*checkResult
}

type test struct {
	took time.Duration
	err  error
}

// fresh samples when this minute has no sample yet, or while Fetch now
// waits for its cycle.
func (h *health) fresh(ctx context.Context) {
	now := h.clock.Now()
	if c := h.checked["chan"]; h.at.IsZero() || !now.Truncate(time.Minute).Equal(h.at.Truncate(time.Minute)) || c != nil && c.Pending {
		h.sample(ctx)
	}
}

func (h *health) sample(ctx context.Context) {
	now := h.clock.Now()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := h.c.Status(ctx)
	server := "running"
	switch {
	case err != nil:
		server = "stopped"
	case !st.Server.Answering:
		server = "not answering"
	}
	if err == nil {
		h.st, h.lastSeen = st, now
		if st.Server.Answering {
			h.tests = map[string]test{}
			for _, dom := range feedDomains(st) {
				took, err := h.dns.Query(ctx, st.Server.Config["server.listen"], dom)
				h.tests[dom] = test{took, err}
			}
		}
	}
	e := &env{st: h.st, now: now, tests: h.tests}
	if e.st == nil {
		e.st = &controlapi.Status{}
	}
	if c := h.checked["chan"]; c != nil && c.Pending {
		h.checked["chan"] = fetched(e.st.Channels, c.At, now)
	}
	e.cfg = e.st.Server.Config
	views := map[string]view{}
	rec := sample{Server: server, F: map[string]mark{}}
	for _, f := range features {
		v := f.eval(e)
		v.Word, v.Actions = words[v.State], f.actions
		if v.State == "off" {
			v.Actions = []action{{"page", "Open its settings", f.page}}
		}
		m := mark{State: v.State}
		if flag(v.State) != "" {
			m.Line = v.Line
		}
		// The server's state wins: nothing works while it's stopped or
		// silent. Facts and dependencies stay as the server last told them.
		if server != "running" {
			v.State, v.Word, v.Impact = "down", serverWords[server], "Readers get nothing from it until the server answers again."
			v.Actions = []action{{"page", "Open Service and power", "svc"}}
			m = mark{State: "down", Line: serverLines[server]}
		}
		views[f.id], rec.F[f.id] = v, m
	}
	since, err := h.store(now, rec)
	if err != nil {
		log.Printf("[admin] health: %v", err)
	}
	if server != "running" {
		for id, v := range views {
			v.Line = "With the server since " + e.when(since["server"])
			views[id] = v
		}
	}
	h.at, h.server, h.views, h.since = now, server, views, since
}

// fetched is Fetch now, asked at asked, as status c says at now: the first
// cycle that started since, or a give-up after 3 intervals without one.
func fetched(c *controlapi.Channels, asked, now time.Time) *checkResult {
	if c == nil {
		return &checkResult{At: now, Summary: "Telegram channels are turned off."}
	}
	every := time.Duration(c.IntervalMs) * time.Millisecond
	for _, cy := range c.Cycles {
		if !cy.Start.Before(asked) {
			return &checkResult{At: now, OK: cycle(cy, every).State == "ok",
				Summary: fmt.Sprintf("%d of %d channels fetched, %s new posts.", cy.Fetched, cy.Total, thousands(int64(cy.Posts)))}
		}
	}
	if wait := time.Duration(limits.chanMissed) * every; now.Sub(asked) > wait {
		return &checkResult{At: now, Summary: "No fetch cycle came back in " + took(wait) + "."}
	}
	return &checkResult{At: asked, Pending: true, Summary: "Fetching…"}
}

func feedDomains(st *controlapi.Status) []string {
	cfg := st.Server.Config
	return list(cfg["dns.domain"] + "," + cfg["dns.extraDomains"])
}

func list(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

var (
	words       = map[string]string{"ok": "Working", "slow": "Slow", "down": "Down", "off": "Off"}
	serverWords = map[string]string{"stopped": "Stopped", "not answering": "Not answering"}
	serverLines = map[string]string{"stopped": "thefeed-server stopped", "not answering": "thefeed-server stopped answering"}
)

// action is a button on a feature's page: check runs its check, edit opens
// the Nitter instance editor, page opens Control's Page.
type action struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Page  string `json:"page,omitempty"`
}

type boardRow struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	State  string  `json:"state"`
	Word   string  `json:"word"`
	Line   string  `json:"line"`
	Uptime *uptime `json:"uptime"`
}

type serverState struct {
	State    string    `json:"state"`
	Since    time.Time `json:"since"`
	Version  string    `json:"version"`
	LastSeen time.Time `json:"lastSeen,omitzero"` // absent until the server first answers
}

func (h *health) handleBoard(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fresh(r.Context())
	out := struct {
		Server    serverState `json:"server"`
		Attention int         `json:"attention"`
		Features  []boardRow  `json:"features"`
	}{Server: serverState{State: h.server, Since: h.since["server"], LastSeen: h.lastSeen}, Features: []boardRow{}}
	if h.st != nil {
		out.Server.Version = h.st.Server.Version
	}
	up := h.uptimes(h.at)
	for _, f := range features {
		v := h.views[f.id]
		if v.State == "slow" || v.State == "down" {
			out.Attention++
		}
		out.Features = append(out.Features, boardRow{ID: f.id, Name: f.name, State: v.State, Word: v.Word, Line: v.Line, Uptime: up[f.id]})
	}
	admin.WriteJSON(w, http.StatusOK, out)
}

func (h *health) handleFeature(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("feature")
	i := slices.IndexFunc(features, func(f feature) bool { return f.id == id })
	if i < 0 {
		admin.WriteError(w, http.StatusNotFound, admin.FieldError{Field: "feature", Message: "There's no feature called " + strconv.Quote(id) + "."})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fresh(r.Context())
	v, up := h.views[id], h.uptimes(h.at)[id]
	if id == "x" && v.State != "off" {
		v.Facts = append(v.Facts, fact{"Uptime, 60 days", percent(up), ""})
	}
	if v.Facts == nil {
		v.Facts = []fact{}
	}
	if v.Deps != nil && v.Deps.Rows == nil { // a list in JSON, even empty
		d := *v.Deps
		d.Rows, v.Deps = [][]string{}, &d
	}
	admin.WriteJSON(w, http.StatusOK, struct {
		ID        string       `json:"id"`
		Name      string       `json:"name"`
		State     string       `json:"state"`
		Word      string       `json:"word"`
		Line      string       `json:"line"`
		Since     time.Time    `json:"since"`
		Impact    string       `json:"impact"`
		Actions   []action     `json:"actions"`
		Facts     []fact       `json:"facts"`
		Uptime    *uptime      `json:"uptime"`
		Deps      *deps        `json:"deps"`
		Incidents []incident   `json:"incidents"`
		LastCheck time.Time    `json:"lastCheck"`
		NextCheck time.Time    `json:"nextCheck"`
		Checked   *checkResult `json:"checked"`
	}{
		ID: id, Name: features[i].name, State: v.State, Word: v.Word, Line: v.Line, Since: h.since[id],
		Impact: v.Impact, Actions: v.Actions, Facts: v.Facts, Uptime: up, Deps: v.Deps, Incidents: h.incidents(id),
		LastCheck: h.at, NextCheck: h.at.Truncate(time.Minute).Add(time.Minute), Checked: h.checked[id],
	})
}

func (h *health) handleCheck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("feature")
	i := slices.IndexFunc(features, func(f feature) bool { return f.id == id })
	if i < 0 {
		admin.WriteError(w, http.StatusNotFound, admin.FieldError{Field: "feature", Message: "There's no feature called " + strconv.Quote(id) + "."})
		return
	}
	ctx := r.Context()
	h.mu.Lock()
	h.fresh(ctx)
	state := h.views[id].State
	h.mu.Unlock()
	if state == "off" {
		admin.WriteError(w, http.StatusConflict, admin.FieldError{Field: "feature", Message: features[i].name + " is turned off."})
		return
	}
	res := &checkResult{At: h.clock.Now()}
	var err error
	switch id {
	case "feed", "dnsm":
		// The panel's own: the feed's test query, the media relay's status.
		h.mu.Lock()
		h.sample(ctx)
		v := h.views[id]
		res.OK, res.Summary = v.State == "ok", v.Line
		switch {
		case h.server != "running":
			res.Summary = serverLines[h.server]
		case id == "feed" && v.State == "ok" && len(v.Deps.Rows) == 1:
			res.Summary = v.Deps.Rows[0][0] + " answered the test query"
		case id == "feed" && v.State == "ok":
			res.Summary = fmt.Sprintf("All %d domains answered the test query", len(v.Deps.Rows))
		}
		h.checked[id] = res
		h.mu.Unlock()
		admin.WriteJSON(w, http.StatusOK, res)
		return
	case "chan":
		// A cycle takes minutes: report it when status has it (see sample).
		err = h.c.Refresh(ctx)
		res.Pending, res.Summary = true, "Fetching…"
	default:
		var cr *controlapi.CheckResult
		if cr, err = h.c.Check(ctx, id); err == nil {
			res.OK, res.Summary, res.Detail = cr.OK, cr.Summary, cr.Detail
		}
	}
	if err != nil {
		admin.WriteError(w, http.StatusBadGateway, admin.FieldError{Message: "Couldn't run the check: " + err.Error()})
		return
	}
	h.mu.Lock()
	h.checked[id] = res
	h.sample(ctx) // the check may have changed what status says
	h.mu.Unlock()
	code := http.StatusOK
	if res.Pending {
		code = http.StatusAccepted
	}
	admin.WriteJSON(w, code, res)
}

// checkResult is the last check the operator ran on a feature.
type checkResult struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Pending bool      `json:"pending,omitempty"` // channels: fetching, no cycle back yet
	Summary string    `json:"summary"`
	Detail  string    `json:"detail,omitempty"`
}

// percent writes an uptime the way the mockup does: 100% or 61.7%.
func percent(u *uptime) string {
	switch {
	case u.Percent == nil:
		return "no days yet"
	case *u.Percent == 100:
		return "100%"
	}
	return strconv.FormatFloat(*u.Percent, 'f', 1, 64) + "%"
}
