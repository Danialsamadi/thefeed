package settings

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// source is one of the server's source lists.
type source struct {
	id      string // in the API: /api/lists/{id}
	label   string // in the action record
	codec   *serverconfig.Codec
	pathKey string // the setting holding its file's path; "" for the Nitter instances, which live in thefeed.env
	file    string // its file in the data directory when pathKey is unset
}

// sources is in reader channel order: public, then private, then X.
var sources = []source{
	{"channels", "public channels", serverconfig.Channels, "chan.file", "channels.txt"},
	{"private", "private channels", serverconfig.PrivateChannels, "chan.privateFile", "private_channels.txt"},
	{"x", "X accounts", serverconfig.XAccounts, "xacc.file", "x_accounts.txt"},
	{"instances", "Nitter instances", serverconfig.Instances, "", ""},
}

const instancesKey = "xacc.instances"

func sourceByID(id string) (source, bool) {
	for _, s := range sources {
		if s.id == id {
			return s, true
		}
	}
	return source{}, false
}

// listFile is one list as it is on disk.
type listFile struct {
	src  source
	path string // "" for the instances
	hash string
	list *serverconfig.List
	// ran is the items the running server uses, and ok whether that's known.
	ran []string
	ok  bool
	// snap is the file as the running server read it, when the panel saw it
	// unchanged since the server started. Discard writes it back.
	snap *snapshot
}

type snapshot struct {
	Started time.Time `json:"started"`
	Content string    `json:"content"`
}

func (m *module) loadList(f *files, src source) (*listFile, error) {
	if src.pathKey == "" {
		s, _ := serverconfig.Lookup(instancesKey)
		l := &listFile{src: src, list: src.codec.Parse([]byte(canon(s, f.saved(s))))}
		if run, ok := f.running[instancesKey]; ok {
			l.ran, l.ok = src.codec.Parse([]byte(canon(s, run))).Items, true
		}
		return l, nil
	}
	s, _ := serverconfig.Lookup(src.pathKey)
	path := f.saved(s)
	if path == "" {
		path = m.path(src.file)
	}
	data, hash, err := serverconfig.ReadFile(path)
	if err != nil {
		return nil, err
	}
	l := &listFile{src: src, path: path, hash: hash, list: src.codec.Parse(data)}
	if src.id == "channels" || f.running == nil {
		return l, nil // public channels apply live, so nothing of theirs waits
	}
	// /status doesn't carry the lists, so the panel keeps a copy of each file
	// as the server read it: the file as it is, if it hasn't changed since the
	// server started.
	//
	// ponytail: a file edited by hand after a start, before the panel looked,
	// has no copy: it shows as waiting and discard leaves it. Add the lists
	// to /status if that matters.
	var snap snapshot
	if found, err := m.ran.Get(src.id, &snap); err != nil || !found || !snap.Started.Equal(f.started) {
		fi, err := os.Stat(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil && fi.ModTime().After(f.started) {
			return l, nil
		}
		snap = snapshot{Started: f.started, Content: string(data)}
		if err := m.ran.Put(src.id, snap); err != nil {
			return nil, err
		}
	}
	l.snap, l.ran, l.ok = &snap, src.codec.Parse([]byte(snap.Content)).Items, true
	return l, nil
}

// waiting is whether the server runs something other than what's saved.
func (l *listFile) waiting(f *files) bool {
	switch {
	case l.src.id == "channels" || f.running == nil:
		return false
	case !l.ok:
		return true
	}
	return !slices.Equal(l.ran, l.list.Items)
}

type itemView struct {
	Name    string `json:"name"`
	Channel int    `json:"channel,omitempty"` // the number readers see it as; 0 for instances
	Waiting bool   `json:"waiting"`
}

type listView struct {
	Applies string     `json:"applies"`
	Waiting bool       `json:"waiting"`
	Items   []itemView `json:"items"`
}

func (f *files) listViews() map[string]listView {
	out := map[string]listView{}
	n := 0 // reader channel numbers run on from one list to the next
	for _, src := range sources {
		l := f.lists[src.id]
		lv := listView{Applies: "restart", Waiting: l.waiting(f), Items: []itemView{}}
		if src.id == "channels" {
			lv.Applies = "live"
		}
		for _, it := range l.list.Items {
			iv := itemView{Name: it, Waiting: l.ok && !slices.Contains(l.ran, it)}
			if src.id != "instances" {
				n++
				iv.Channel = n
			}
			lv.Items = append(lv.Items, iv)
		}
		out[src.id] = lv
	}
	return out
}

// listResult is what a list change answers: the new state, which list
// changed (an invite link added as a channel lands in "private"), and
// whether the server already runs it.
type listResult struct {
	stateView
	List        string `json:"list"`
	Live        bool   `json:"live"`
	ReloadError string `json:"reloadError,omitempty"`
}

func (m *module) handleList(w http.ResponseWriter, r *http.Request) {
	src, ok := sourceByID(r.PathValue("list"))
	if !ok {
		admin.WriteError(w, http.StatusNotFound, admin.FieldError{Field: "list", Message: "There's no list called " + r.PathValue("list") + "."})
		return
	}
	var req change
	if !admin.ReadJSON(w, r, &req) {
		return
	}
	f, err := m.load(r.Context())
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't read the settings: " + err.Error()})
		return
	}
	if src.id == "channels" && req.Op == "add" && serverconfig.LooksLikeInvite(req.Value) {
		src, _ = sourceByID("private")
	}
	if src.id == "private" && req.Op == "add" && !m.telegram(f).LoggedIn {
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "value", Message: "Private channels need the Telegram login, which is logged out."})
		return
	}
	l := f.lists[src.id]
	items := l.list.Items
	if req.Op != "add" && (req.Index < 0 || req.Index >= len(items) || req.Op == "move" && (req.To < 0 || req.To >= len(items))) {
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "index", Message: "That entry is no longer on the list. Reload and try again."})
		return
	}
	var did string
	switch req.Op {
	case "add", "rename":
		old, item := "", ""
		if req.Op == "add" {
			item, err = l.list.Add(req.Value)
		} else {
			old = items[req.Index]
			item, err = l.list.Rename(req.Index, req.Value)
		}
		if err != nil {
			admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "value", Message: err.Error()})
			return
		}
		did = "Added " + l.show(item) + " to " + src.label
		if req.Op == "rename" {
			did = "Renamed " + l.show(old) + " to " + l.show(item) + " in " + src.label
		}
	case "move":
		item := items[req.Index]
		l.list.Items = slices.Insert(slices.Delete(items, req.Index, req.Index+1), req.To, item)
		did = "Moved " + l.show(item) + " to place " + strconv.Itoa(req.To+1) + " in " + src.label
	case "remove":
		if src.id == "instances" && len(items) == 1 {
			admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "index", Message: "Keep at least one instance: X is read through them."})
			return
		}
		did = "Removed " + l.show(items[req.Index]) + " from " + src.label
		l.list.Items = slices.Delete(items, req.Index, req.Index+1)
	default:
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: "op", Message: "Use add, rename, move or remove."})
		return
	}
	if err := m.saveList(f, l, req.Base); err != nil {
		m.writeFailed(w, err)
		return
	}
	res := listResult{List: src.id}
	outcome := "Saved, applies after a restart"
	if src.id == "channels" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := m.c.ReloadChannels(ctx); err != nil {
			res.ReloadError = err.Error()
			outcome = "Saved, but the server didn't reload it: " + err.Error()
		} else {
			res.Live = true
			outcome = "Applied live"
		}
	}
	m.d.Record(admin.Action{Action: did, Outcome: outcome})
	if f, err = m.load(r.Context()); err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Saved, but couldn't read it back: " + err.Error()})
		return
	}
	res.stateView = m.view(f)
	admin.WriteJSON(w, http.StatusOK, res)
}

// show is how the action record names an item. Invite links stay out of it.
func (l *listFile) show(item string) string {
	switch l.src.id {
	case "channels", "x":
		return "@" + item
	case "private":
		return "an invite link"
	}
	return item
}

// saveList writes l back: its own file, or thefeed.env for the instances.
func (m *module) saveList(f *files, l *listFile, base map[string]string) error {
	if l.path != "" {
		return serverconfig.WriteFile(l.path, l.list.Bytes(), base[l.src.id])
	}
	s, _ := serverconfig.Lookup(instancesKey)
	if err := f.env.Set(s.Env, strings.TrimSpace(string(l.list.Bytes()))); err != nil {
		return err
	}
	return serverconfig.WriteFile(admin.EnvFile(m.d.ServerDataDir), f.env.Bytes(), base["env"])
}
