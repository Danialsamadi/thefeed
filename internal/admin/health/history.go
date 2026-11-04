package health

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The health bucket holds, under key prefixes so one sample is one write:
//
//	s/<UTC minute>           a sample: the server's state and each feature's
//	d/<local day>            a day's rollup: each feature's state that day
//	i/<feature>/<UTC start>  an incident
const (
	samplePrefix = "s/"
	dayPrefix    = "d/"
)

// sample is one minute's states.
type sample struct {
	Server string          `json:"server"`
	F      map[string]mark `json:"f"`
}

type mark struct {
	State string `json:"s"`
	Line  string `json:"l,omitempty"` // only when slow or down, for incidents
}

const minute = "2006-01-02T15:04Z"

func sampleKey(t time.Time) []byte { return []byte(samplePrefix + t.UTC().Format(minute)) }
func dayKey(t time.Time) []byte    { return []byte(dayPrefix + t.Format(time.DateOnly)) }

// midnight is the start of t's day in t's own time zone.
func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// store writes the sample taken at now, opens and closes incidents, and
// rolls its day up again. It returns since when each feature, and the
// server under "server", has been in its state: as far back as samples go.
func (h *health) store(now time.Time, s sample) (map[string]time.Time, error) {
	since := map[string]time.Time{"server": now}
	same := map[string]bool{"server": true} // still the same, going back
	for id := range s.F {
		since[id], same[id] = now, true
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return since, err
	}
	return since, h.db.Update(func(b *bolt.Bucket) error {
		key := sampleKey(now)
		if err := b.Put(key, raw); err != nil {
			return err
		}
		if err := prune(b, now); err != nil {
			return err
		}
		var prev *sample
		var prevAt time.Time
		c := b.Cursor()
		c.Seek(key)
		for k, v := c.Prev(); len(same) > 0 && k != nil && bytes.HasPrefix(k, []byte(samplePrefix)); k, v = c.Prev() {
			var old sample
			at, err := time.Parse(minute, string(k[len(samplePrefix):]))
			if err != nil || json.Unmarshal(v, &old) != nil {
				continue
			}
			if prev == nil {
				prev, prevAt = &old, at
			}
			if same["server"] = same["server"] && old.Server == s.Server; same["server"] {
				since["server"] = at
			}
			for id, m := range s.F {
				if same[id] = same[id] && old.F[id].State == m.State; same[id] {
					since[id] = at
				}
			}
			for id, ok := range same {
				if !ok {
					delete(same, id)
				}
			}
		}
		if prev != nil {
			if err := track(b, prevAt, *prev, s); err != nil {
				return err
			}
		}
		return rollUp(b, now)
	})
}

// prune drops samples older than 7 days, and rollups and finished
// incidents older than 90.
func prune(b *bolt.Bucket, now time.Time) error {
	samples, days := sampleKey(now.Add(-7*24*time.Hour)), dayKey(now.AddDate(0, 0, -90))
	cutoff := now.AddDate(0, 0, -90)
	var gone [][]byte
	c := b.Cursor()
	for k, v := c.First(); k != nil; k, v = c.Next() {
		var inc incident
		switch {
		case bytes.HasPrefix(k, []byte(samplePrefix)):
			if bytes.Compare(k, samples) >= 0 {
				// Samples are last in the bucket, oldest first: done.
				return deleteKeys(b, gone)
			}
		case bytes.HasPrefix(k, []byte(dayPrefix)):
			if bytes.Compare(k, days) >= 0 {
				continue
			}
		case json.Unmarshal(v, &inc) != nil || inc.End == nil || !inc.End.Before(cutoff):
			continue
		}
		gone = append(gone, append([]byte(nil), k...))
	}
	return deleteKeys(b, gone)
}

// deleteKeys deletes after the cursor is done: deleting under a live bbolt
// cursor can skip the next key.
func deleteKeys(b *bolt.Bucket, keys [][]byte) error {
	for _, k := range keys {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	return nil
}

// incident is a stretch of a feature not being ok. End is nil while it lasts.
type incident struct {
	Start   time.Time  `json:"start"`
	End     *time.Time `json:"end"`
	Worst   string     `json:"worst"` // slow or down
	Summary string     `json:"summary"`
}

func incidentPrefix(id string) []byte { return []byte("i/" + id + "/") }

var rank = map[string]int{"slow": 1, "down": 2}

// track opens an incident on the second sample in a row that is slow or
// down, and closes it on the second ok (or off) one. prev, taken at
// prevAt, is the sample before cur.
func track(b *bolt.Bucket, prevAt time.Time, prev, cur sample) error {
	for id, m := range cur.F {
		p := prev.F[id]
		key, inc := openIncident(b, id)
		switch {
		case inc == nil && rank[m.State] > 0 && rank[p.State] > 0:
			worst := p
			if rank[m.State] > rank[p.State] {
				worst = m
			}
			key = append(incidentPrefix(id), prevAt.UTC().Format(time.RFC3339)...)
			inc = &incident{Start: prevAt, Worst: worst.State, Summary: worst.Line}
		case inc != nil && rank[m.State] > rank[inc.Worst]:
			inc.Worst, inc.Summary = m.State, m.Line
		case inc != nil && rank[m.State] == 0 && rank[p.State] == 0:
			inc.End = &prevAt
		default:
			continue
		}
		raw, err := json.Marshal(inc)
		if err != nil {
			return err
		}
		if err := b.Put(key, raw); err != nil {
			return err
		}
	}
	return nil
}

// openIncident is feature id's incident that is still going, if any: it
// can only be the newest.
func openIncident(b *bolt.Bucket, id string) ([]byte, *incident) {
	prefix := incidentPrefix(id)
	c := b.Cursor()
	c.Seek(append(append([]byte(nil), prefix...), 0xff))
	k, v := c.Prev()
	var inc incident
	if k == nil || !bytes.HasPrefix(k, prefix) || json.Unmarshal(v, &inc) != nil || inc.End != nil {
		return nil, nil
	}
	return append([]byte(nil), k...), &inc
}

// incidents are feature id's incidents, newest first.
func (h *health) incidents(id string) []incident {
	out := []incident{}
	prefix := incidentPrefix(id)
	h.db.View(func(b *bolt.Bucket) error {
		c := b.Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			var inc incident
			if json.Unmarshal(v, &inc) == nil {
				out = append(out, inc)
			}
		}
		return nil
	})
	slices.Reverse(out)
	return out
}

// rollUp recounts the minutes of now's day from its samples: off all day
// is off, 30 minutes down is down, any slow or down is slow, else up.
func rollUp(b *bolt.Bucket, now time.Time) error {
	day := midnight(now)
	from, to := sampleKey(day), sampleKey(day.AddDate(0, 0, 1))
	total := 0
	count := map[string]map[string]int{} // feature, state: minutes
	c := b.Cursor()
	for k, v := c.Seek(from); k != nil && bytes.Compare(k, to) < 0; k, v = c.Next() {
		var s sample
		if json.Unmarshal(v, &s) != nil {
			continue
		}
		total++
		for id, m := range s.F {
			if count[id] == nil {
				count[id] = map[string]int{}
			}
			count[id][m.State]++
		}
	}
	states := map[string]string{}
	for id, n := range count {
		switch {
		case n["off"] == total:
			states[id] = "off"
		case n["down"] >= limits.dayDownMinutes:
			states[id] = "down"
		case n["down"]+n["slow"] > 0:
			states[id] = "slow"
		default:
			states[id] = "up"
		}
	}
	raw, err := json.Marshal(states)
	if err != nil {
		return err
	}
	return b.Put(dayKey(now), raw)
}

// uptime is a feature's last 60 days, today last. A day without samples
// counts as off: there's nothing to say about it.
type uptime struct {
	Days    []string `json:"days"` // up, slow, down or off
	Percent *float64 `json:"percent"`
	Up      int      `json:"up"`
	Slow    int      `json:"slow"`
	Down    int      `json:"down"`
	Off     int      `json:"off"`
}

// uptimes reads every feature's last 60 days up to now's.
func (h *health) uptimes(now time.Time) map[string]*uptime {
	out := map[string]*uptime{}
	for _, f := range features {
		out[f.id] = &uptime{}
	}
	today := midnight(now)
	h.db.View(func(b *bolt.Bucket) error {
		for i := 59; i >= 0; i-- {
			var states map[string]string
			if raw := b.Get(dayKey(today.AddDate(0, 0, -i))); raw != nil {
				json.Unmarshal(raw, &states)
			}
			for id, u := range out {
				s := states[id]
				if s == "" {
					s = "off"
				}
				u.Days = append(u.Days, s)
				switch s {
				case "up":
					u.Up++
				case "slow":
					u.Slow++
				case "down":
					u.Down++
				default:
					u.Off++
				}
			}
		}
		return nil
	})
	// Uptime is the days that weren't down of the days that weren't off.
	for _, u := range out {
		if on := u.Up + u.Slow + u.Down; on > 0 {
			p := math.Round(float64(u.Up+u.Slow)/float64(on)*1000) / 10
			u.Percent = &p
		}
	}
	return out
}
