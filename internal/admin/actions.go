package admin

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Action is one entry in the action record: what the operator did, and how
// it went. For example {Action: "Restart", Outcome: "Back up in 9 seconds"}.
type Action struct {
	At      time.Time `json:"at"`
	Action  string    `json:"action"`
	Outcome string    `json:"outcome"`
}

const keepActions = 365 * 24 * time.Hour

// record appends a under the bucket's next sequence number, so keys run in
// the order actions happened, and drops entries older than a year.
func (a *App) record(act Action) error {
	now := a.clock.Now()
	if act.At.IsZero() {
		act.At = now
	}
	raw, err := json.Marshal(act)
	if err != nil {
		return err
	}
	return a.actions.Update(func(b *bolt.Bucket) error {
		seq, err := b.NextSequence()
		if err != nil {
			return err
		}
		if err := b.Put(binary.BigEndian.AppendUint64(nil, seq), raw); err != nil {
			return err
		}
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.First() {
			var old Action
			if json.Unmarshal(v, &old) == nil && now.Sub(old.At) < keepActions {
				break
			}
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func (a *App) handleActions(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	limit = min(limit, 500)
	out := []Action{}
	err = a.actions.View(func(b *bolt.Bucket) error {
		c := b.Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			var act Action
			if err := json.Unmarshal(v, &act); err != nil {
				return err
			}
			out = append(out, act)
		}
		return nil
	})
	if err != nil {
		WriteError(w, http.StatusInternalServerError, FieldError{Message: "Couldn't read the action record."})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"actions": out})
}
