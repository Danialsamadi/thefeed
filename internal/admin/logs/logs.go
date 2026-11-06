// Package logs is the panel's view of thefeed-server's systemd journal: each
// feature's own log and the whole server log, newest first, grouped by day.
//
// Each line gets a feature from the [tag] the server logs it with (the
// features table) and a level from keyword rules (the levels table), since
// the server's lines carry no level.
//
// # API
//
//	GET /api/logs/server       every line
//	GET /api/logs/{feature}    one feature's lines: x, tg, feed, chan, chat, dnsm, gh
//
//	GET /api/logs/{feature}/export   the same lines as a .log file to save
//
// All take ?filter=all|bad (bad: warnings and errors only) and ?q= (lines
// with those words, in any case). The first two also take ?limit=N
// (default 100, at most 500) and ?cursor= from the page before, and answer
//
//	{"days":[{"day":"2026-11-04","lines":[{"at","level","feature","text"}]}],"next":"..."}
//
// level is INFO, WARN or ERROR; feature is "" for lines that belong to no
// feature page. Days are in the panel's time zone. next is "" when the
// journal has nothing older.
//
// An export takes ?hours=1|24|168 and answers text/plain as an attachment:
// a "# " line saying what it holds, then each line oldest first as
// "2026-11-04 12:01:30 UTC ERROR text": at most the newest 50,000 lines,
// and a second "# " line when that left some out.
//
// With THEFEED_ADMIN_SERVER=docker the lines come from the server
// container's log instead, through the Docker Engine API, and next is
// built from a line's time.
package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
)

// Journal reads thefeed-server's journal as `journalctl --output=json`
// prints it: up to n entries, newest first, starting at the entry cursor
// names (inclusive), or at the newest when cursor is "".
type Journal interface {
	Read(ctx context.Context, cursor string, n int) ([]byte, error)
}

// journalctl is the real Journal.
type journalctl struct{}

func (journalctl) Read(ctx context.Context, cursor string, n int) ([]byte, error) {
	// --all: without it JSON output turns fields over 4096 bytes, such as
	// [dns_hourly] reports, into null.
	args := []string{"--unit=thefeed-server", "--output=json", "--all", "--reverse", "--quiet", "--no-pager", "--lines=" + strconv.Itoa(n)}
	if cursor != "" {
		args = append(args, "--cursor="+cursor)
	}
	out, err := admin.HostCommand(ctx, "journalctl", args...).Output()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return nil, fmt.Errorf("journalctl: %s", bytes.TrimSpace(ee.Stderr))
	}
	return out, err
}

// Module is the logs module on the real journal, or in Docker mode on the
// server container's log (see admin.ServerContainer).
func Module(d *admin.Deps) error {
	docker, name, err := admin.ServerContainer()
	if err != nil {
		return err
	}
	if docker != nil {
		return New(Container(docker, name))(d)
	}
	return New(journalctl{})(d)
}

// New is the logs module reading j.
func New(j Journal) admin.Module {
	return func(d *admin.Deps) error {
		d.Handle("GET /api/logs/{feature}", func(w http.ResponseWriter, r *http.Request) {
			s, ok := asked(w, r)
			if !ok {
				return
			}
			limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
			if err != nil || limit <= 0 {
				limit = 100
			}
			limit = min(limit, 500)
			lines, next, err := readPage(r.Context(), j, d.Clock.Now().Location(), r.URL.Query().Get("cursor"), limit, s.keep)
			if err != nil {
				admin.WriteError(w, http.StatusServiceUnavailable, admin.FieldError{Message: "Couldn't read thefeed-server's journal: " + err.Error()})
				return
			}
			p := byDay(lines)
			p.Next = next
			admin.WriteJSON(w, http.StatusOK, p)
		})
		d.Handle("GET /api/logs/{feature}/export", func(w http.ResponseWriter, r *http.Request) {
			s, ok := asked(w, r)
			if !ok {
				return
			}
			hours, err := strconv.Atoi(r.URL.Query().Get("hours"))
			if err != nil || !slices.Contains([]int{1, 24, 168}, hours) {
				admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "hours", Message: "Export the last 1, 24 or 168 hours."})
				return
			}
			now := d.Clock.Now()
			since := now.Add(-time.Duration(hours) * time.Hour)
			lines, cut, err := readSince(r.Context(), j, now.Location(), since, s.keep)
			if err != nil {
				admin.WriteError(w, http.StatusServiceUnavailable, admin.FieldError{Message: "Couldn't read thefeed-server's journal: " + err.Error()})
				return
			}
			var b strings.Builder
			fmt.Fprintf(&b, "# thefeed-server log%s, %s to %s, oldest first: %d %s\n", s.about(), since.Format(minuteLayout), now.Format(minuteLayout), len(lines), map[bool]string{true: "line", false: "lines"}[len(lines) == 1])
			if cut {
				b.WriteString("# Cut short: the oldest lines in the range were left out.\n")
			}
			for _, l := range lines {
				fmt.Fprintf(&b, "%s %-5s %s\n", l.At.Format("2006-01-02 15:04:05 MST"), l.Level, l.Text)
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="thefeed-%s-%s.log"`, s.feature, now.Format("2006-01-02-1504")))
			io.WriteString(w, b.String())
		})
		return nil
	}
}

const minuteLayout = "2006-01-02 15:04 MST"

// selection is which lines a request asks for.
type selection struct {
	feature string // or "server" for every line
	bad     bool   // warnings and errors only
	q       string // lower case; "" for any
}

// asked reads the feature, ?filter= and ?q= of r, or answers it when
// they're wrong.
func asked(w http.ResponseWriter, r *http.Request) (selection, bool) {
	s := selection{feature: r.PathValue("feature"), q: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))}
	if !slices.Contains(slices.Collect(maps.Values(features)), s.feature) && s.feature != "server" {
		admin.WriteError(w, http.StatusNotFound, admin.FieldError{Field: "feature", Message: "There's no feature called " + strconv.Quote(s.feature) + "."})
		return s, false
	}
	switch r.URL.Query().Get("filter") {
	case "", "all":
	case "bad":
		s.bad = true
	default:
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "filter", Message: `The filter is "all" or "bad" (warnings and errors).`})
		return s, false
	}
	return s, true
}

func (s selection) keep(l line) bool {
	return (s.feature == "server" || l.Feature == s.feature) && !(s.bad && l.Level == "INFO") &&
		(s.q == "" || strings.Contains(strings.ToLower(l.Text), s.q))
}

// about says what an export holds, after "thefeed-server log".
func (s selection) about() string {
	out := ""
	if s.feature != "server" {
		out += ", feature " + s.feature
	}
	if s.bad {
		out += ", warnings and errors"
	}
	if s.q != "" {
		out += ", matching " + strconv.Quote(s.q)
	}
	return out
}

// maxExport caps an export's lines, and maxExportReads its journal reads.
const (
	maxExport      = 50_000
	maxExportReads = 100
)

// readSince reads the journal back from the newest entry to since, for up to
// maxExport lines that keep accepts, and gives them oldest first. cut is
// true when the cap left older lines in the range out.
func readSince(ctx context.Context, j Journal, loc *time.Location, since time.Time, keep func(line) bool) (out []line, cut bool, err error) {
	const chunk = 2000
	cursor := ""
	defer func() { slices.Reverse(out) }()
	for range maxExportReads {
		raw, err := j.Read(ctx, cursor, chunk)
		if err != nil {
			return nil, false, err
		}
		lines, err := parse(raw, loc)
		if err != nil {
			return nil, false, err
		}
		got := len(lines)
		if cursor != "" && got > 0 && lines[0].cursor == cursor {
			lines = lines[1:]
		}
		for _, l := range lines {
			cursor = l.cursor
			if l.At.Before(since) {
				return out, false, nil
			}
			if keep(l) {
				if out = append(out, l); len(out) == maxExport {
					return out, true, nil
				}
			}
		}
		if got < chunk {
			return out, false, nil
		}
	}
	return out, true, nil
}

// maxReads caps the journal reads behind one page.
const maxReads = 25

// readPage reads the journal back from cursor ("" = the newest entry) for up
// to limit lines that keep accepts. next is the cursor to go on from, or ""
// when the journal has nothing older.
//
// A scan, not an index. A rare feature's page may come back short,
// even empty, with a next cursor after 25 reads of 4 pages' worth (10,000
// entries at the default limit), e.g. under --debug's per-query lines;
// journalctl --grep per feature is the upgrade if that bites.
func readPage(ctx context.Context, j Journal, loc *time.Location, cursor string, limit int, keep func(line) bool) (out []line, next string, err error) {
	chunk := 4 * limit
	for range maxReads {
		raw, err := j.Read(ctx, cursor, chunk)
		if err != nil {
			return nil, "", err
		}
		lines, err := parse(raw, loc)
		if err != nil {
			return nil, "", err
		}
		got := len(lines)
		// journalctl --cursor starts at the cursor's own entry, which the
		// page before already had.
		if cursor != "" && got > 0 && lines[0].cursor == cursor {
			lines = lines[1:]
		}
		for _, l := range lines {
			cursor = l.cursor
			if keep(l) {
				if out = append(out, l); len(out) == limit {
					return out, cursor, nil
				}
			}
		}
		if got < chunk {
			return out, "", nil
		}
	}
	return out, cursor, nil
}

type line struct {
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Feature string    `json:"feature"`
	Text    string    `json:"text"`
	cursor  string
}

type day struct {
	Day   string `json:"day"`
	Lines []line `json:"lines"`
}

type page struct {
	Days []day  `json:"days"`
	Next string `json:"next"`
}

// goTime is the date and time Go's log package writes in front of each line.
var goTime = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d(\.\d+)? `)

// parse reads journalctl's JSON output, one entry per object.
func parse(raw []byte, loc *time.Location) ([]line, error) {
	var out []line
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var e struct {
			Cursor   string          `json:"__CURSOR"`
			Realtime string          `json:"__REALTIME_TIMESTAMP"`
			Message  json.RawMessage `json:"MESSAGE"`
		}
		if err := dec.Decode(&e); errors.Is(err, io.EOF) {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		us, err := strconv.ParseInt(e.Realtime, 10, 64)
		if err != nil {
			return nil, err
		}
		text := goTime.ReplaceAllString(message(e.Message), "")
		out = append(out, line{At: time.UnixMicro(us).In(loc), Level: levelOf(text), Feature: featureOf(text), Text: text, cursor: e.Cursor})
	}
}

// message is MESSAGE as text: journalctl prints it as a string, or as an
// array of bytes when it isn't printable UTF-8.
func message(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b []byte
	json.Unmarshal(raw, &b)
	return string(b)
}

// features maps the first [tag] of a line, as internal/server logs it, to
// the feature page it shows on, by the mockup's feature ids. Lines with any
// other tag, or none ([server], [version], systemd's own lines), are only
// in the server log.
var features = map[string]string{
	"[dns]": "feed", "[dns_hourly]": "feed", "[extrablock]": "feed", "[meta]": "feed",
	"[telegram]": "chan", "[public]": "chan", "[admin]": "chan", "[profile-pics]": "chan", "[public profile-pic]": "chan",
	"[x]": "x", "[x profile-pic]": "x",
	"[mtproto]":     "tg",
	"[chat]":        "chat",
	"[media-debug]": "dnsm",
	"[gh-relay]":    "gh",
}

// levels classifies a line: the server's lines carry no level, so the first
// rule whose words appear in the line (any case) wins, and a line no rule
// matches is INFO. Lines ending in an error value (": %v") are judged by
// the error's own words, which is why common Go error texts are here.
var levels = []struct{ words, level string }{
	// Reports and summaries that mention failure words without being one.
	{"[dns_hourly] {", "INFO"},
	{"cycle done", "INFO"},
	// Success lines that carry channel and account names and titles, which
	// can hold any word ("Bad Calls", @lostfilm).
	{"] updated ", "INFO"},
	{"added channel @", "INFO"},
	{"removed channel @", "INFO"},
	{"joined private channel", "INFO"},
	{"sent message to", "INFO"},
	// One Nitter instance failing is a warning; all of them failing is the error.
	{": instance ", "WARN"},

	{"failed", "ERROR"},
	{"failure", "ERROR"},
	{"error", "ERROR"},
	{"panic", "ERROR"},
	{"cannot write", "ERROR"},
	{"repo full", "ERROR"},
	{"dropping", "ERROR"},
	{"denied", "ERROR"},
	{"no space", "ERROR"},
	{"bad ", "ERROR"},

	{"rejected", "WARN"},
	{"refus", "WARN"},
	{"timeout", "WARN"},
	{"deadline exceeded", "WARN"},
	{"slow down", "WARN"},
	{"budget", "WARN"},
	{"skipping", "WARN"},
	{"lost", "WARN"},
	{"evicted", "WARN"},
	{"missing", "WARN"},
	{"invalid", "WARN"},
	{"no such", "WARN"},
	{"eof", "WARN"},
	{"reset", "WARN"},
	{"unreachable", "WARN"},
}

func levelOf(text string) string {
	text = strings.ToLower(text)
	for _, r := range levels {
		if strings.Contains(text, r.words) {
			return r.level
		}
	}
	return "INFO"
}

func featureOf(text string) string {
	tag, _, ok := strings.Cut(text, "]")
	if !ok || !strings.HasPrefix(tag, "[") {
		return ""
	}
	return features[tag+"]"]
}

// byDay groups lines, newest first, by their day in the panel's time zone.
func byDay(lines []line) page {
	p := page{Days: []day{}}
	for _, l := range lines {
		d := l.At.Format(time.DateOnly)
		if n := len(p.Days); n == 0 || p.Days[n-1].Day != d {
			p.Days = append(p.Days, day{Day: d})
		}
		p.Days[len(p.Days)-1].Lines = append(p.Days[len(p.Days)-1].Lines, l)
	}
	return p
}
