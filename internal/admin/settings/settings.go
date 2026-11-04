// Package settings is the panel's Control data: thefeed-server's settings,
// its source lists and its domains, read from and written to the files the
// server reads at start (thefeed.env and the list files), through
// internal/serverconfig.
//
// A change "waits" when the saved value or list differs from the one the
// running server reports in /status. Public channels apply live: after
// writing channels.txt the panel asks the server to reload it, and they
// wait only when that fails.
//
// # API
//
//	GET   /api/settings                  the state
//	PATCH /api/settings/{key}            {value, base} → state
//	POST  /api/settings/discard          {base} → state, the files back to what the server runs
//	POST  /api/settings/github-token     {value, base} → state; the token is never sent back
//	POST  /api/settings/telegram-logout  {base} → state + restartNeeded
//	POST  /api/lists/{list}              {op, value, index, to, base} → state + list, live, reloadError
//	GET   /api/domains                   {domains:[{domain, serves, main}]}
//	POST  /api/domains/check             {checkedAt, domains:[{domain, serves, main, ns, pointsHere, error}]}
//
// The state is
//
//	{base:{env,channels,private,x}, server, waiting, telegram:{loggedIn, sessionSaved},
//	 settings:[{key, page, type, min, max, allowed, default, applies, secret, set, saved, running, waiting}],
//	 lists:{channels|private|x|instances: {applies, waiting, items:[{name, channel, waiting}]}}}
//
// base holds each file's hash; every write sends it back, and is refused
// with 409 when the file changed since. server is false when thefeed-server
// doesn't answer; running is then missing and nothing waits. Secrets have
// set instead of values. {list} is channels, private, x or instances; op is
// add, rename, move or remove, index and to count from 0, and an invite
// link added to channels lands in private. channel is the number readers
// see an entry as.
package settings

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/controlapi"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// DNS is what the NS check asks the network: the NS records of a domain and
// the addresses of a host, both from public resolvers, and the addresses
// that count as this server.
type DNS interface {
	NS(ctx context.Context, domain string) ([]string, error)
	Addrs(ctx context.Context, host string) ([]string, error)
	Here(ctx context.Context) ([]string, error)
}

// Module is the settings module on the real control socket and resolvers.
func Module(d *admin.Deps) error {
	return New(d.Control(), publicDNS{})(d)
}

// New is the settings module talking to c and dns.
func New(c controlapi.Client, dns DNS) admin.Module {
	return func(d *admin.Deps) error {
		m := &module{d: d, c: c, dns: dns}
		d.Handle("GET /api/settings", m.handleGet)
		d.Handle("PATCH /api/settings/{key}", m.handlePatch)
		d.Handle("POST /api/settings/discard", m.handleDiscard)
		d.Handle("POST /api/settings/github-token", m.handleToken)
		d.Handle("POST /api/settings/telegram-logout", m.handleLogout)
		d.Handle("GET /api/domains", m.handleDomains)
		d.Handle("POST /api/domains/check", m.handleCheck)
		d.Handle("POST /api/lists/{list}", m.handleList)
		return nil
	}
}

type module struct {
	d   *admin.Deps
	c   controlapi.Client
	dns DNS
}

func (m *module) path(name string) string { return filepath.Join(m.d.ServerDataDir, name) }

// files is what's on disk, read once per request.
type files struct {
	env      *serverconfig.Env
	envHash  string
	lists    map[string]*listFile
	running  map[string]string // nil when the server doesn't answer
	runLists controlapi.Lists
}

func (m *module) load(ctx context.Context) (*files, error) {
	data, hash, err := serverconfig.ReadFile(admin.EnvFile(m.d.ServerDataDir))
	if err != nil {
		return nil, err
	}
	f := &files{env: serverconfig.ParseEnv(data), envHash: hash, lists: map[string]*listFile{}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if st, err := m.c.Status(ctx); err == nil {
		f.running, f.runLists = st.Server.Config, st.Lists
	}
	for _, src := range sources {
		if f.lists[src.id], err = m.loadList(f, src); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// telegram is the server's Telegram login: logged in when thefeed.env
// doesn't turn it off and a session is saved.
func (m *module) telegram(f *files) telegramView {
	var v telegramView
	if fi, err := os.Stat(m.sessionPath(f)); err == nil && fi.Size() > 0 {
		at := fi.ModTime()
		v.SessionSaved = &at
		off, _ := serverconfig.Lookup("fetch.noTelegram")
		v.LoggedIn = canon(off, f.saved(off)) == "false"
	}
	return v
}

// handleLogout logs the server out of Telegram: no login in thefeed.env,
// and no saved session. It takes effect at the next restart (ticket 12's).
func (m *module) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req change
	if !admin.ReadJSON(w, r, &req) {
		return
	}
	s, _ := serverconfig.Lookup("fetch.noTelegram")
	if !m.setEnv(w, r, req.Base, s, "true") {
		return
	}
	f, err := m.load(r.Context())
	if err == nil {
		if err = os.Remove(m.sessionPath(f)); os.IsNotExist(err) {
			err = nil
		}
	}
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Turned the login off, but couldn't delete the session: " + err.Error()})
		return
	}
	m.d.Record(admin.Action{Action: "Logged out of Telegram", Outcome: "Session deleted, restart needed"})
	if f, err = m.load(r.Context()); err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Logged out, but couldn't read the settings back: " + err.Error()})
		return
	}
	admin.WriteJSON(w, http.StatusOK, struct {
		stateView
		RestartNeeded bool `json:"restartNeeded"`
	}{m.view(f), true})
}

func (m *module) sessionPath(f *files) string {
	s, _ := serverconfig.Lookup("fetch.session")
	if p := f.saved(s); p != "" {
		return p
	}
	return m.path("session.json")
}

// saved is key's value in thefeed.env, or its default when unset: the
// server ignores an empty value.
func (f *files) saved(s serverconfig.Setting) string {
	if v, _ := f.env.Get(s.Env); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return s.Default
}

// canon is v as the server would run it, so "yes" and "true", "010" and
// "10", 0 and a ZeroIsDefault setting's default, or no Nitter instances and
// the server's two defaults compare equal.
func canon(s serverconfig.Setting, v string) string {
	if v = strings.TrimSpace(v); v == "" {
		v = s.Default
	}
	switch {
	case s.Type == serverconfig.TypeBool:
		if b, ok := serverconfig.ParseBool(v); ok {
			return strconv.FormatBool(b)
		}
	case s.Type == serverconfig.TypeInt:
		if n, err := strconv.Atoi(v); err == nil && n == 0 && s.ZeroIsDefault {
			return s.Default
		} else if err == nil {
			return strconv.Itoa(n)
		}
	case s.Key == "xacc.instances" && v == "":
		return defaultInstances
	}
	return v
}

const tokenKey = "media.ghToken"

// settingWaiting is whether s is saved as something other than what the
// server runs. Secrets never are: /status leaves them out. Nor are the
// settings the panel doesn't show: a unit's --data-dir, for one, runs
// whatever thefeed.env says, and the panel can't change them anyway.
func (f *files) settingWaiting(s serverconfig.Setting) bool {
	run, ok := f.running[s.Key]
	return ok && !s.Secret && s.Page != "" && canon(s, f.saved(s)) != canon(s, run)
}

// defaultInstances is what the server reads X through when no instance is
// set (normalizeXRSSInstances in internal/server/xpublic.go).
const defaultInstances = "https://nitter.net,http://nitter.net"

type settingView struct {
	Key     string   `json:"key"`
	Page    string   `json:"page"`
	Type    string   `json:"type"`
	Min     int      `json:"min,omitempty"`
	Max     int      `json:"max,omitempty"`
	Allowed []string `json:"allowed,omitempty"`
	Default string   `json:"default"`
	Applies string   `json:"applies"`
	Secret  bool     `json:"secret,omitempty"`
	Set     bool     `json:"set,omitempty"` // secrets only: thefeed.env has a value
	Saved   *string  `json:"saved,omitempty"`
	Running *string  `json:"running,omitempty"`
	Waiting bool     `json:"waiting"`
}

type stateView struct {
	Base     map[string]string   `json:"base"`
	Server   bool                `json:"server"` // the running server answered
	Waiting  int                 `json:"waiting"`
	Settings []settingView       `json:"settings"`
	Lists    map[string]listView `json:"lists"`
	Telegram telegramView        `json:"telegram"`
}

type telegramView struct {
	LoggedIn     bool       `json:"loggedIn"`
	SessionSaved *time.Time `json:"sessionSaved,omitempty"`
}

func (m *module) view(f *files) stateView {
	v := stateView{Base: map[string]string{"env": f.envHash}, Server: f.running != nil, Settings: []settingView{}, Lists: f.listViews(),
		Telegram: m.telegram(f)}
	for id, l := range f.lists {
		if l.path != "" {
			v.Base[id] = l.hash
		}
	}
	for _, s := range serverconfig.Settings {
		sv := settingView{Key: s.Key, Page: s.Page, Type: string(s.Type), Min: s.Min, Max: s.Max, Allowed: s.Allowed,
			Default: s.Default, Applies: s.Applies, Secret: s.Secret}
		if s.Secret {
			sv.Set = f.saved(s) != ""
		} else {
			saved := f.saved(s)
			sv.Saved = &saved
			if run, ok := f.running[s.Key]; ok {
				sv.Running = &run
			}
			sv.Waiting = f.settingWaiting(s)
		}
		if sv.Waiting {
			v.Waiting++
		}
		v.Settings = append(v.Settings, sv)
	}
	// The instances are a setting, counted above.
	for _, id := range []string{"channels", "private", "x"} {
		if v.Lists[id].Waiting {
			v.Waiting++
		}
	}
	return v
}

func (m *module) handleGet(w http.ResponseWriter, r *http.Request) {
	f, err := m.load(r.Context())
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't read the settings: " + err.Error()})
		return
	}
	admin.WriteJSON(w, http.StatusOK, m.view(f))
}

// change is the body of every write: the new value, and the base hashes
// from the state the operator was looking at.
type change struct {
	Value string            `json:"value"`
	Base  map[string]string `json:"base"`
	// Lists only: what to do, to which item (by its place, from 0) and,
	// for a move, its new place.
	Op    string `json:"op"`
	Index int    `json:"index"`
	To    int    `json:"to"`
}

func (m *module) handlePatch(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	var req change
	if !admin.ReadJSON(w, r, &req) {
		return
	}
	s, ok := serverconfig.Lookup(key)
	switch {
	case !ok:
		admin.WriteError(w, http.StatusNotFound, admin.FieldError{Field: key, Message: "There's no setting called " + strconv.Quote(key) + "."})
		return
	case key == tokenKey:
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: key, Message: "Replace the GitHub token with its own form."})
		return
	case s.Secret || s.Page == "":
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: key, Message: "This setting can't be changed from the panel."})
		return
	}
	if err := serverconfig.Validate(key, req.Value); err != nil {
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: key, Message: err.Error()})
		return
	}
	value := strings.TrimSpace(req.Value)
	if m.setEnv(w, r, req.Base, s, value) {
		m.d.Record(admin.Action{Action: "Set " + key + " to " + value, Outcome: "Saved, applies after a restart"})
		m.handleGet(w, r)
	}
}

// githubToken is the shape of a GitHub token: classic or fine-grained.
var githubToken = regexp.MustCompile(`^(ghp_|github_pat_)\w{20,}$`)

// handleToken replaces the GitHub token. It's never sent back, nor written
// to the action record.
func (m *module) handleToken(w http.ResponseWriter, r *http.Request) {
	var req change
	if !admin.ReadJSON(w, r, &req) {
		return
	}
	value := strings.TrimSpace(req.Value)
	if !githubToken.MatchString(value) {
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: tokenKey, Message: "That doesn't look like a GitHub token. Tokens start with ghp_ or github_pat_."})
		return
	}
	s, _ := serverconfig.Lookup(tokenKey)
	if m.setEnv(w, r, req.Base, s, value) {
		m.d.Record(admin.Action{Action: "Replaced the GitHub token", Outcome: "Saved, applies after a restart"})
		m.handleGet(w, r)
	}
}

// setEnv saves s as value in thefeed.env, if the file is still the one
// base names. It answers the request itself when it fails.
func (m *module) setEnv(w http.ResponseWriter, r *http.Request, base map[string]string, s serverconfig.Setting, value string) bool {
	f, err := m.load(r.Context())
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't read the settings: " + err.Error()})
		return false
	}
	if err := f.env.Set(s.Env, value); err != nil {
		admin.WriteError(w, http.StatusBadRequest, admin.FieldError{Field: s.Key, Message: "That value has a space, quote or other character thefeed.env can't hold."})
		return false
	}
	if err := serverconfig.WriteFile(admin.EnvFile(m.d.ServerDataDir), f.env.Bytes(), base["env"]); err != nil {
		m.writeFailed(w, err)
		return false
	}
	return true
}

// handleDiscard rewrites thefeed.env and the list files to what the
// server runs. Secrets are left alone: /status doesn't say what they are.
func (m *module) handleDiscard(w http.ResponseWriter, r *http.Request) {
	var req change
	if !admin.ReadJSON(w, r, &req) {
		return
	}
	f, err := m.load(r.Context())
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't read the settings: " + err.Error()})
		return
	}
	if f.running == nil {
		admin.WriteError(w, http.StatusConflict, admin.FieldError{Message: "thefeed-server isn't answering, so there's no running configuration to go back to."})
		return
	}
	// Check every base first, so a refusal writes nothing.
	stale := req.Base["env"] != f.envHash
	n := 0
	for _, s := range serverconfig.Settings {
		if f.settingWaiting(s) {
			n++
			if err := f.env.Set(s.Env, f.running[s.Key]); err != nil {
				admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Field: s.Key, Message: "Couldn't write the running value back: " + err.Error()})
				return
			}
		}
	}
	var back []*listFile
	for _, l := range f.lists {
		if l.path != "" && l.waiting() {
			back = append(back, l)
			stale = stale || req.Base[l.src.id] != l.hash
		}
	}
	if stale {
		m.writeFailed(w, serverconfig.ErrChangedElsewhere)
		return
	}
	if n > 0 {
		if err := serverconfig.WriteFile(admin.EnvFile(m.d.ServerDataDir), f.env.Bytes(), req.Base["env"]); err != nil {
			m.writeFailed(w, err)
			return
		}
	}
	for _, l := range back {
		l.list.Items = l.running()
		if err := serverconfig.WriteFile(l.path, l.list.Bytes(), req.Base[l.src.id]); err != nil {
			m.writeFailed(w, err)
			return
		}
	}
	if n+len(back) > 0 {
		m.d.Record(admin.Action{Action: "Discarded " + strconv.Itoa(n+len(back)) + " waiting changes", Outcome: "The files match the running server again"})
	}
	m.handleGet(w, r)
}

// writeFailed answers a failed file write.
func (m *module) writeFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, serverconfig.ErrChangedElsewhere) {
		admin.WriteError(w, http.StatusConflict, admin.FieldError{Message: "Changed elsewhere since you opened this page. Reload and try again."})
		return
	}
	admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't save: " + err.Error()})
}
