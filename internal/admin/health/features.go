package health

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sartoopjj/thefeed/internal/controlapi"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// view is one feature as the last sample saw it.
type view struct {
	State   string // ok, slow, down or off
	Word    string // the state in words: Working, Slow, Down, Off, Stopped, Not answering
	Line    string // the Board's one fact; for slow and down, why
	Impact  string // what it means for readers
	Actions []action
	Facts   []fact // the "right now" numbers
	Deps    *deps  // what it depends on; nil when off
}

// fact is one "right now" number. State is "", or slow or down to flag it.
type fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
	State string `json:"state"`
}

// deps is a feature's dependency table. A "Status" column holds a state:
// ok, slow, down or pending (not tried yet).
type deps struct {
	Title string     `json:"title"`
	Cols  []string   `json:"cols"`
	Rows  [][]string `json:"rows"`
}

// flag is state when it's a problem, else "".
func flag(state string) string {
	if state == "slow" || state == "down" {
		return state
	}
	return ""
}

// env is what a feature's rules read: the last status, the feed
// self-test and the panel's time.
type env struct {
	st    *controlapi.Status
	cfg   map[string]string
	tests map[string]test
	now   time.Time
}

// limits are the rules' thresholds: the spec's starting values.
var limits = struct {
	feedSlow       time.Duration // a self-test answer slower than this is slow
	chanMissed     int           // intervals without a good cycle before channels are down
	chanSlowTook   float64       // a cycle taking more of the interval than this is slow
	chanSlowShare  float64       // a cycle fetching fewer of the channels than this is slow
	tgDownWindow   time.Duration // every dial failing in this window is down
	tgNearCap      float64       // users at this share of the cap is slow
	ghFailStreak   int           // uploads failing in a row before the relay is down
	ghWaiting      int           // more uploads waiting than this is slow
	ghBudget       float64       // the repository over this percent of its budget is slow
	dayDownMinutes int           // a day with this many minutes down is a down day
}{
	feedSlow:       time.Second,
	chanMissed:     3,
	chanSlowTook:   0.8,
	chanSlowShare:  0.9,
	tgDownWindow:   10 * time.Minute,
	tgNearCap:      0.9,
	ghFailStreak:   3,
	ghWaiting:      50,
	ghBudget:       90,
	dayDownMinutes: 30,
}

// off is a feature turned off in Control.
var off = view{State: "off", Line: "Turned off in Control", Impact: "Turned off in Control. Readers' apps show it as turned off."}

type feature struct {
	id, name string
	eval     func(*env) view
	actions  []action // its buttons while it's on
	page     string   // the Control page that turns it on
}

// features are the panel's features, in the Board's order, by the ids the
// logs module uses too.
var features = []feature{
	{"x", "X (Twitter)", evalX, []action{{"check", "Check now", ""}, {"edit", "Edit instances", ""}}, "xacc"},
	{"tg", "Telegram over DNS", evalTG, []action{{"check", "Check data centres", ""}, {"page", "Tunnel settings", "mt"}}, "mt"},
	{"feed", "DNS feed", evalFeed, []action{{"check", "Send a test query", ""}}, "dns"},
	{"chan", "Telegram channels", evalChan, []action{{"check", "Fetch now", ""}}, "fetch"},
	{"chat", "Messenger", evalChat, []action{{"check", "Send a test message", ""}}, "chat"},
	// The server has no media check, so this one samples again.
	{"dnsm", "DNS media relay", evalMedia, []action{{"check", "Check now", ""}}, "media"},
	{"gh", "GitHub media relay", evalGH, []action{{"check", "Check GitHub", ""}}, "media"},
}

func evalX(e *env) view {
	x := e.st.X
	if x == nil || x.Accounts == 0 {
		return view{State: "off", Line: "No X accounts", Impact: "Add X accounts in Control to read X."}
	}
	// An instance resting after HTTP 429 isn't failing: it asked to be left
	// alone for a while, and the others take its turns.
	ok, tried, resting := 0, 0, 0
	d := &deps{Title: "Nitter instances", Cols: []string{"Instance", "Status", "Result"}}
	for _, in := range x.Instances {
		row := []string{in.Host, "ok", fmt.Sprintf("answered in %s", took(time.Duration(in.LatencyMs)*time.Millisecond))}
		switch {
		case in.At.IsZero():
			row[1], row[2] = "pending", "not checked yet"
		case in.RestUntil.After(e.now):
			row[1], row[2] = "resting", "rate limited (HTTP 429), asked again at "+e.when(in.RestUntil)
			resting++
		case !in.OK:
			row[1], row[2] = "down", in.Error
		}
		if !in.At.IsZero() && !in.RestUntil.After(e.now) {
			tried++
		}
		if in.OK {
			ok++
		}
		d.Rows = append(d.Rows, row)
	}
	v := view{State: "ok", Line: fmt.Sprintf("%d of %d Nitter instances answer", ok, len(x.Instances)), Deps: d}
	if resting > 0 {
		v.Line += fmt.Sprintf(", %d resting", resting)
	}
	switch {
	case tried == 0 && resting == 0:
		v.Line = "Nitter instances not checked yet"
	case ok == 0 && tried > 0:
		v.State = "down"
	case ok < tried, ok == 0:
		v.State = "slow"
	}
	accounts := strconv.Itoa(x.Accounts)
	if v.State == "down" {
		accounts += ", paused"
	}
	lastPost := "not yet"
	if !x.LastPost.IsZero() {
		lastPost = e.when(x.LastPost)
	}
	// The handler adds "Uptime, 60 days": it comes from the history.
	v.Facts = []fact{
		{"Instances answering", fmt.Sprintf("%d of %d", ok, len(x.Instances)), flag(v.State)},
		{"X accounts", accounts, ""},
		{"Last X post fetched", lastPost, ""},
	}
	v.Impact = map[string]string{
		"ok":   "X posts reach readers.",
		"slow": "X posts still arrive, through the instances that answer.",
		"down": "X accounts are paused. Telegram channels are not affected.",
	}[v.State]
	return v
}

func evalTG(e *env) view {
	m := e.st.MTProto
	if m == nil {
		return off
	}
	recent, recentOK, fails, failDC := 0, 0, 0, map[int]bool{}
	slowest := -1
	d := &deps{Title: "Telegram data centres", Cols: []string{"Data centre", "Status", "Dial time"}}
	for i, dc := range m.DCs {
		dial := took(time.Duration(dc.DialMs) * time.Millisecond)
		row := []string{fmt.Sprintf("DC %d", dc.ID), "ok", dial}
		switch {
		case dc.At.IsZero():
			row[1], row[2] = "pending", "not dialled yet"
		case !dc.OK:
			row[1], row[2] = "down", dc.Error
		case dc.FailuresLastHour > 0:
			row[1], row[2] = "slow", fmt.Sprintf("%s, %s in the last hour", dial, plural(dc.FailuresLastHour, "failure", "failures"))
		}
		d.Rows = append(d.Rows, row)
		if !dc.At.IsZero() && e.now.Sub(dc.At) <= limits.tgDownWindow {
			recent++
			if dc.OK {
				recentOK++
			}
		}
		if dc.FailuresLastHour > 0 {
			fails += dc.FailuresLastHour
			failDC[dc.ID] = true
		}
		if dc.OK && (slowest < 0 || dc.DialMs > m.DCs[slowest].DialMs) {
			slowest = i
		}
	}
	nearCap := m.MaxTunnels > 0 && float64(m.Sessions) >= limits.tgNearCap*float64(m.MaxTunnels)
	users := fmt.Sprintf("%d, no cap", m.Sessions)
	if m.MaxTunnels > 0 {
		users = fmt.Sprintf("%d of %d", m.Sessions, m.MaxTunnels)
	}
	slowestDC := "none answered yet"
	if slowest >= 0 {
		slowestDC = fmt.Sprintf("DC %d, %s", m.DCs[slowest].ID, took(time.Duration(m.DCs[slowest].DialMs)*time.Millisecond))
	}
	done := func(v view) view {
		v.Deps = d
		v.Facts = []fact{
			{"Tunnels open", thousands(int64(m.LiveTunnels)), ""},
			{"Users connected", users, flagIf(nearCap, "slow")},
			{"Dial failures, last hour", strconv.Itoa(fails), flagIf(fails > 0, "slow")},
			{"Slowest data centre", slowestDC, ""},
		}
		v.Impact = map[string]string{
			"ok":   "People can use Telegram over DNS.",
			"slow": "People using Telegram over DNS may wait longer.",
			"down": "People using Telegram over DNS can't reach Telegram.",
		}[v.State]
		return v
	}
	switch {
	case recent > 0 && recentOK == 0:
		return done(view{State: "down", Line: "Every data centre dial failed in the last 10 minutes"})
	case len(failDC) == 1:
		for id := range failDC {
			return done(view{State: "slow", Line: fmt.Sprintf("%s to data centre %d in the last hour", plural(fails, "dial failure", "dial failures"), id)})
		}
	case fails > 0:
		return done(view{State: "slow", Line: plural(fails, "dial failure", "dial failures") + " in the last hour"})
	case nearCap:
		return done(view{State: "slow", Line: fmt.Sprintf("%d of %d users connected, near the cap", m.Sessions, m.MaxTunnels)})
	}
	return done(view{State: "ok", Line: fmt.Sprintf("%d users connected, %d tunnels open", m.Sessions, m.LiveTunnels)})
}

func evalFeed(e *env) view {
	doms := feedDomains(e.st)
	perMin, total := map[string]int64{}, int64(0)
	for _, d := range e.st.Feed.Domains {
		if slices.Contains(doms, d.Domain) {
			perMin[d.Domain] = d.PerMinute
			total += d.Queries
		}
	}
	var qpm int64
	var failed []string
	slowest := ""
	d := &deps{Title: "Domains", Cols: []string{"Domain", "Status", "Queries a minute", "Test query"}}
	for _, dom := range doms {
		qpm += perMin[dom]
		t, tested := e.tests[dom]
		row := []string{dom, "ok", thousands(perMin[dom]), took(t.took)}
		switch {
		case !tested:
			row[1], row[3] = "pending", "not sent yet"
		case t.err != nil:
			row[1], row[3] = "down", t.err.Error()
			failed = append(failed, dom)
		case t.took > limits.feedSlow:
			row[1] = "slow"
		}
		if tested && t.err == nil && (slowest == "" || t.took > e.tests[slowest].took) {
			slowest = dom
		}
		d.Rows = append(d.Rows, row)
	}
	slow := fact{"Slowest test query", "not sent yet", ""}
	if slowest != "" {
		slow.Value, slow.State = took(e.tests[slowest].took), flagIf(e.tests[slowest].took > limits.feedSlow, "slow")
	}
	v := view{State: "ok", Line: fmt.Sprintf("%s queries a minute on %s", thousands(qpm), plural(len(doms), "domain", "domains")), Deps: d}
	v.Impact = fmt.Sprintf("Readers are getting the feed on all %d domains.", len(doms))
	if len(doms) == 1 {
		v.Impact = "Readers are getting the feed on " + doms[0] + "."
	}
	switch {
	case len(failed) > 0:
		v.State, v.Line = "down", "No answer to the test query on "+strings.Join(failed, ", ")
		v.Impact = "Readers on " + strings.Join(failed, ", ") + " get nothing."
	case slow.State != "":
		v.State, v.Line = "slow", fmt.Sprintf("The test query took %s on %s", slow.Value, slowest)
		v.Impact = "Readers get the feed, but slowly."
	}
	v.Facts = []fact{
		{"Queries a minute", thousands(qpm), ""},
		{"Queries since the server started", thousands(total), ""},
		{"Domains answering", fmt.Sprintf("%d of %d", len(doms)-len(failed), len(doms)), flagIf(len(failed) > 0, "down")},
		slow,
	}
	return v
}

func evalChan(e *env) view {
	c := e.st.Channels
	if c == nil {
		return off
	}
	every := time.Duration(c.IntervalMs) * time.Millisecond
	d := &deps{Title: "Recent cycles", Cols: []string{"Started", "Channels", "Posts", "Took"}}
	for _, cy := range slices.Backward(c.Cycles) {
		d.Rows = append(d.Rows, []string{e.when(cy.Start), fmt.Sprintf("%d of %d", cy.Fetched, cy.Total), thousands(int64(cy.Posts)), took(time.Duration(cy.DurationMs) * time.Millisecond)})
	}
	facts := []fact{{"Channels, last cycle", "none yet", ""}, {"Posts, last cycle", "none yet", ""}, {"Cycle took", "none yet", ""}, {"Next cycle", "soon", ""}}
	if n := len(c.Cycles); n > 0 {
		last := c.Cycles[n-1]
		dur := time.Duration(last.DurationMs) * time.Millisecond
		share := float64(last.Fetched) / float64(max(last.Total, 1))
		facts = []fact{
			{"Channels, last cycle", fmt.Sprintf("%d of %d", last.Fetched, last.Total), flagIf(last.Fetched == 0, "down") + flagIf(last.Fetched > 0 && share < limits.chanSlowShare, "slow")},
			{"Posts, last cycle", thousands(int64(last.Posts)), ""},
			{"Cycle took", took(dur), flagIf(float64(dur) > limits.chanSlowTook*float64(every), "slow")},
			{"Next cycle", e.when(last.Start.Add(every)), ""},
		}
	}
	done := func(v view) view {
		v.Facts, v.Deps = facts, d
		v.Impact = map[string]string{
			"ok":   "Readers get new posts within about " + took(every) + ".",
			"slow": "New posts reach readers late, or some are missing.",
			"down": "Readers get no new posts.",
		}[v.State]
		return v
	}
	good := e.st.Server.Started // no good cycle yet: count from the start
	for _, cy := range c.Cycles {
		if cy.Fetched > 0 {
			good = cy.Start
		}
	}
	if since := e.now.Sub(good); since > time.Duration(limits.chanMissed)*every {
		return done(view{State: "down", Line: "No channels fetched in the last " + took(since.Truncate(time.Minute))})
	}
	if len(c.Cycles) == 0 {
		return done(view{State: "ok", Line: "Waiting for the first fetch cycle"})
	}
	last := c.Cycles[len(c.Cycles)-1]
	if v := cycle(last, every); v.State != "ok" {
		return done(v)
	}
	end := last.Start.Add(time.Duration(last.DurationMs) * time.Millisecond)
	return done(view{State: "ok", Line: fmt.Sprintf("%s, last cycle done at %s", plural(last.Total, "channel", "channels"), e.when(end))})
}

// cycle judges one fetch cycle on its own.
func cycle(c controlapi.FetchCycle, every time.Duration) view {
	d := time.Duration(c.DurationMs) * time.Millisecond
	switch {
	case c.Fetched == 0:
		return view{State: "down", Line: fmt.Sprintf("The last cycle fetched none of the %d channels", c.Total)}
	case float64(d) > limits.chanSlowTook*float64(every):
		return view{State: "slow", Line: fmt.Sprintf("The last cycle took %s of the %s interval", took(d), took(every))}
	case float64(c.Fetched) < limits.chanSlowShare*float64(c.Total):
		return view{State: "slow", Line: fmt.Sprintf("The last cycle fetched %d of %d channels", c.Fetched, c.Total)}
	}
	return view{State: "ok"}
}

func evalChat(e *env) view {
	c := e.st.Chat
	if c == nil {
		return off
	}
	v := view{State: "ok", Line: fmt.Sprintf("%s addresses active today, %s messages", thousands(int64(c.ActiveToday)), thousands(c.MessagesToday)),
		Impact: "Messages are stored and delivered."}
	if c.StoreErrors > 0 {
		v.State, v.Line = "down", plural(int(c.StoreErrors), "message store error", "message store errors")+" in the last 10 minutes"
		v.Impact = "Messages may be lost or not delivered."
	}
	v.Facts = []fact{
		{"Active addresses today", thousands(int64(c.ActiveToday)), ""},
		{"Messages today", thousands(c.MessagesToday), ""},
		{"Waiting for pickup", thousands(int64(c.Waiting)), ""},
		{"Inboxes full today", thousands(c.InboxFullToday), ""},
	}
	v.Deps = &deps{Title: "Limits", Cols: []string{"Limit", "Value"}, Rows: [][]string{
		{"Sends an hour, per address", e.cfg["chat.sendPerHour"]},
		{"Inbox size", e.cfg["chat.inboxCap"]},
		{"Waiting per pair", e.cfg["chat.perPairCap"]},
		{"Message size", e.cfg["chat.maxMsgBytes"] + " bytes"},
		{"Undelivered messages expire after", e.cfg["chat.ttlHours"] + " h"},
	}}
	return v
}

func evalMedia(e *env) view {
	m := e.st.MediaCache
	if on, _ := serverconfig.ParseBool(e.cfg["media.dnsOn"]); !on {
		return off
	}
	d := &deps{Title: "Settings", Cols: []string{"Setting", "Value"}, Rows: [][]string{
		{"Compression", e.cfg["media.dnsComp"]},
		{"Cache time", e.cfg["media.dnsTTLMin"] + " min"},
		{"Max file size", e.cfg["media.dnsMaxKB"] + " KB"},
	}}
	switch {
	case m == nil:
		return view{State: "down", Line: "The media cache isn't running", Impact: "Readers can't load images and files over DNS.", Deps: d}
	case m.Errors > 0:
		return view{State: "down", Line: plural(int(m.Errors), "file the media cache couldn't store", "files the media cache couldn't store") + " in the last 10 minutes",
			Impact: "Readers can't load those images and files over DNS.", Deps: d}
	}
	return view{State: "ok", Line: fmt.Sprintf("%s files cached, %s block queries served", thousands(m.Entries), thousands(int64(m.Queries))),
		Impact: "Readers can load images and files up to " + size(m.MaxFileBytes) + " over DNS.",
		Facts: []fact{
			{"Files cached", thousands(m.Entries), ""},
			{"Cache size", size(m.Bytes), ""},
			{"Block queries served", thousands(int64(m.Queries)), ""},
			{"Max file size", size(m.MaxFileBytes), ""},
		}, Deps: d}
}

func evalGH(e *env) view {
	g := e.st.GHRelay
	if g == nil {
		return off
	}
	failing, waiting, full := g.FailStreak >= limits.ghFailStreak, g.PendingFiles > limits.ghWaiting, g.PercentUsed > limits.ghBudget
	why := "failed in a row"
	if g.LastError != "" {
		why += ": " + g.LastError
	}
	repo := []string{"Repository", flagIf(full, "slow"), fmt.Sprintf("%s, branch %s, %.0f%% of its budget", g.Repo, e.cfg["media.ghBranch"], g.PercentUsed)}
	token := []string{"Token", flagIf(g.TokenDenied, "down"), "works"}
	quota := []string{"Quota", flagIf(g.Quota403, "down"), "not used up"}
	uploads := []string{"Uploads", flagIf(waiting, "slow"), thousands(int64(g.PendingFiles)) + " waiting"}
	if g.TokenDenied {
		token[2] = "refused"
	}
	if g.Quota403 {
		quota[2] = "used up"
	}
	if failing {
		uploads[1], uploads[2] = "down", fmt.Sprintf("%d %s", g.FailStreak, why)
	}
	repoSize := size(g.RepoSizeKB << 10)
	if g.QuotaKB > 0 {
		repoSize += " of " + size(g.QuotaKB<<10)
	}
	v := view{
		Facts: []fact{
			{"Uploads waiting", thousands(int64(g.PendingFiles)), flagIf(waiting, "slow")},
			{"Repository size", repoSize, repo[1]},
			{"Token", token[2], token[1]},
			{"Failures in a row", strconv.Itoa(g.FailStreak), flagIf(failing, "down")},
		},
		Deps: &deps{Title: "GitHub", Cols: []string{"Check", "Status", "Detail"}, Rows: [][]string{repo, token, quota, uploads}},
	}
	for _, r := range v.Deps.Rows {
		if r[1] == "" {
			r[1] = "ok"
		}
	}
	switch {
	case g.TokenDenied:
		v.State, v.Line = "down", "GitHub refuses the token"
	case g.Quota403:
		v.State, v.Line = "down", "The GitHub quota is used up"
	case failing:
		v.State, v.Line = "down", fmt.Sprintf("%d uploads %s", g.FailStreak, why)
	case waiting:
		v.State, v.Line = "slow", fmt.Sprintf("%s uploads waiting", thousands(int64(g.PendingFiles)))
	case full:
		v.State, v.Line = "slow", fmt.Sprintf("The repository is at %.0f%% of its budget", g.PercentUsed)
	default:
		v.State, v.Line = "ok", fmt.Sprintf("%s uploads waiting, repository at %.0f%% of its budget", thousands(int64(g.PendingFiles)), g.PercentUsed)
	}
	v.Impact = map[string]string{
		"ok":   "Larger media reaches readers through GitHub.",
		"slow": "Larger media may reach readers late.",
		"down": "Larger media doesn't reach readers through GitHub.",
	}[v.State]
	return v
}

// flagIf is s if cond holds, else "": a fact's or row's flag.
func flagIf(cond bool, s string) string {
	if cond {
		return s
	}
	return ""
}

// when is t as the operator reads it: the time today, else the day too.
func (e *env) when(t time.Time) string {
	t = t.In(e.now.Location())
	if y, m, d := t.Date(); y == e.now.Year() && m == e.now.Month() && d == e.now.Day() {
		return t.Format("15:04")
	}
	return t.Format("2 Jan, 15:04")
}

// took writes a duration the way the mockup does: 88 ms, 9.8 s, 3 min 12 s.
func took(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return strconv.FormatFloat(math.Round(d.Seconds()*10)/10, 'f', -1, 64) + " s"
	case d%time.Minute < time.Second:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d min %d s", int(d.Minutes()), int(d.Seconds())%60)
}

// size writes a byte count: 500 bytes, 200 KB, 1.5 MB, 12 GB.
func size(b int64) string {
	if b < 1<<10 {
		return fmt.Sprintf("%d bytes", b)
	}
	n, unit := float64(b)/(1<<10), "KB"
	for _, u := range []string{"MB", "GB", "TB"} {
		if n < 1<<10 {
			break
		}
		n, unit = n/(1<<10), u
	}
	return strconv.FormatFloat(math.Round(n*10)/10, 'f', -1, 64) + " " + unit
}

// thousands writes 2304 as "2,304".
func thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
