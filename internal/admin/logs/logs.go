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
// Both take ?filter=all|bad (bad: warnings and errors only), ?limit=N
// (default 100, at most 500) and ?cursor= from the page before, and answer
//
//	{"days":[{"day":"2026-11-04","lines":[{"at","level","feature","text"}]}],"next":"..."}
//
// level is INFO, WARN or ERROR; feature is "" for lines that belong to no
// feature page. Days are in the panel's time zone. next is "" when the
// journal has nothing older.
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

// Module is the logs module on the real journal.
func Module(d *admin.Deps) error { return New(journalctl{})(d) }

// New is the logs module reading j.
func New(j Journal) admin.Module {
	return func(d *admin.Deps) error {
		d.Handle("GET /api/logs/{feature}", func(w http.ResponseWriter, r *http.Request) {
			feature := r.PathValue("feature")
			if !slices.Contains(slices.Collect(maps.Values(features)), feature) && feature != "server" {
				admin.WriteError(w, http.StatusNotFound, admin.FieldError{Field: "feature", Message: "There's no feature called " + strconv.Quote(feature) + "."})
				return
			}
			bad := false
			switch r.URL.Query().Get("filter") {
			case "", "all":
			case "bad":
				bad = true
			default:
				admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "filter", Message: `The filter is "all" or "bad" (warnings and errors).`})
				return
			}
			limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
			if err != nil || limit <= 0 {
				limit = 100
			}
			limit = min(limit, 500)
			lines, next, err := readPage(r.Context(), j, d.Clock.Now().Location(), r.URL.Query().Get("cursor"), limit, func(l line) bool {
				return (feature == "server" || l.Feature == feature) && !(bad && l.Level == "INFO")
			})
			if err != nil {
				admin.WriteError(w, http.StatusServiceUnavailable, admin.FieldError{Message: "Couldn't read thefeed-server's journal: " + err.Error()})
				return
			}
			p := byDay(lines)
			p.Next = next
			admin.WriteJSON(w, http.StatusOK, p)
		})
		return nil
	}
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
