package settings_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/settings"
	"github.com/sartoopjj/thefeed/internal/controlapi"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

const origin = "https://panel.test"

// started is when the fake thefeed-server started. Files the test writes
// are dated an hour before it, as if the server read them at start; files
// the panel writes get the real time, which is after it.
var started = time.Now().Add(-time.Hour).Truncate(time.Second)

// fakeServer is thefeed-server's control socket. config holds the running
// values the test changes; the rest are the registry defaults, as the
// server reports them when thefeed.env doesn't set them. lists is the
// source lists it runs; a reload reads channels.txt in dir into them,
// unless reloadErr says it failed.
type fakeServer struct {
	config    map[string]string
	lists     controlapi.Lists
	dir       string
	down      bool
	reloads   int
	reloadErr error
}

func (f *fakeServer) Status(context.Context) (*controlapi.Status, error) {
	if f.down {
		return nil, errors.New("dial unix control.sock: connect: no such file or directory")
	}
	cfg := map[string]string{}
	for _, s := range serverconfig.Settings {
		if !s.Secret {
			cfg[s.Key] = s.Default
		}
	}
	for k, v := range f.config {
		cfg[k] = v
	}
	return &controlapi.Status{Server: controlapi.Server{Version: "v0.41.0", Started: started, Answering: true, Config: cfg}, Lists: f.lists}, nil
}

func (f *fakeServer) ReloadChannels(context.Context) error {
	f.reloads++
	if f.reloadErr != nil {
		return f.reloadErr
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "channels.txt"))
	f.lists.Channels = nil
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			f.lists.Channels = append(f.lists.Channels, strings.TrimPrefix(line, "@"))
		}
	}
	return err
}
func (f *fakeServer) Refresh(context.Context) error { panic("not used") }
func (f *fakeServer) SetAnswering(context.Context, bool) error {
	panic("not used")
}
func (f *fakeServer) Check(context.Context, string) (*controlapi.CheckResult, error) {
	panic("not used")
}

// fakeDNS answers NS checks from tables.
type fakeDNS struct {
	ns    map[string][]string
	addrs map[string][]string
	here  []string
}

func (f *fakeDNS) NS(_ context.Context, domain string) ([]string, error) {
	if ns, ok := f.ns[domain]; ok {
		return ns, nil
	}
	return nil, errors.New("lookup " + domain + ": no such host")
}

func (f *fakeDNS) Addrs(_ context.Context, host string) ([]string, error) {
	return f.addrs[host], nil
}

func (f *fakeDNS) Here(context.Context) ([]string, error) { return f.here, nil }

type panel struct {
	t      *testing.T
	dir    string // thefeed-server's data directory
	srv    *fakeServer
	dns    *fakeDNS
	now    time.Time
	app    *admin.App
	cookie *http.Cookie
}

// newPanel is the real admin app with the settings module, signed in, on a
// data directory holding files (name → content), each dated before the
// server started.
func newPanel(t *testing.T, files map[string]string) *panel {
	t.Helper()
	p := &panel{t: t, dir: t.TempDir(), srv: &fakeServer{config: map[string]string{}}, dns: &fakeDNS{},
		now: time.Date(2026, 11, 4, 12, 5, 0, 0, time.UTC)}
	p.srv.dir = p.dir
	for name, content := range files {
		p.write(name, content, started.Add(-time.Hour))
	}
	cfg := admin.Config{AdminDir: t.TempDir(), ServerDataDir: p.dir, Clock: admin.ClockFunc(func() time.Time { return p.now })}
	app, err := admin.New(cfg, settings.New(p.srv, p.dns))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	p.app = app
	code, err := os.ReadFile(filepath.Join(cfg.AdminDir, "setup-code"))
	if err != nil {
		t.Fatal(err)
	}
	w := p.do("POST", "/api/auth/setup", map[string]string{"code": strings.TrimSpace(string(code)), "username": "admin",
		"password": "correct horse battery", "repeat": "correct horse battery"})
	for _, c := range w.Result().Cookies() {
		if c.Name == "thefeed_admin" && c.Value != "" {
			p.cookie = c
		}
	}
	if p.cookie == nil {
		t.Fatalf("setup = %d %s", w.Code, w.Body)
	}
	return p
}

// write puts a file in the data directory, dated at.
func (p *panel) write(name, content string, at time.Time) {
	p.t.Helper()
	path := filepath.Join(p.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		p.t.Fatal(err)
	}
}

func (p *panel) read(name string) string {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join(p.dir, name))
	if err != nil {
		p.t.Fatal(err)
	}
	return string(b)
}

// do sends a request the way the panel's page does: JSON with a matching
// Origin on anything that changes state.
func (p *panel) do(method, path string, body any) *httptest.ResponseRecorder {
	var r *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, origin+path, r)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
	}
	if p.cookie != nil {
		req.AddCookie(p.cookie)
	}
	w := httptest.NewRecorder()
	p.app.Handler().ServeHTTP(w, req)
	return w
}

// state is GET /api/settings, and what every change answers with.
type state struct {
	Base     map[string]string
	Server   bool
	Waiting  int
	Telegram struct{ LoggedIn bool }
	Settings []struct {
		Key, Page, Type, Default, Applies string
		Min, Max                          int
		Secret, Set                       bool
		Saved, Running                    *string
		Waiting                           bool
	}
	Lists map[string]struct {
		Applies string
		Waiting bool
		Items   []struct {
			Name    string
			Channel int
			Waiting bool
		}
	}
	Live          bool
	List          string
	ReloadError   string
	RestartNeeded bool
}

func (p *panel) state() state {
	p.t.Helper()
	return p.decode(p.do("GET", "/api/settings", nil), http.StatusOK)
}

func (p *panel) decode(w *httptest.ResponseRecorder, code int) state {
	p.t.Helper()
	if w.Code != code {
		p.t.Fatalf("status = %d, want %d: %s", w.Code, code, w.Body)
	}
	var s state
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		p.t.Fatalf("%v in %s", err, w.Body)
	}
	return s
}

// setting finds key in s.
func (s state) setting(t *testing.T, key string) (saved, running string, waiting bool) {
	t.Helper()
	for _, x := range s.Settings {
		if x.Key == key {
			if x.Saved != nil {
				saved = *x.Saved
			}
			if x.Running != nil {
				running = *x.Running
			}
			return saved, running, x.Waiting
		}
	}
	t.Fatalf("no setting %s", key)
	return
}

const token = "ghp_oldTokenOldTokenOldToken123456"

const envFile = `# thefeed-server settings, written by install.sh
THEFEED_DOMAIN=t.example.com
THEFEED_FETCH_INTERVAL=10
THEFEED_GITHUB_RELAY_TOKEN=` + token + `
# hand-added, not a thefeed setting
MY_NOTE=keep
`

// envPanel is a panel on envFile, with the server running it.
func envPanel(t *testing.T) *panel {
	p := newPanel(t, map[string]string{"thefeed.env": envFile})
	p.srv.config["dns.domain"] = "t.example.com"
	return p
}

func TestReadSettingsShowsSavedAndRunningButNeverASecret(t *testing.T) {
	p := envPanel(t)

	w := p.do("GET", "/api/settings", nil)
	if strings.Contains(w.Body.String(), token) {
		t.Fatalf("the token is in the response: %s", w.Body)
	}
	s := p.decode(w, http.StatusOK)

	if saved, running, waiting := s.setting(t, "fetch.every"); saved != "10" || running != "10" || waiting {
		t.Errorf("fetch.every = saved %q, running %q, waiting %v; want 10, 10, false", saved, running, waiting)
	}
	// Not in thefeed.env: the server runs it at its default, so nothing waits.
	if saved, running, waiting := s.setting(t, "chat.inboxCap"); saved != "50" || running != "50" || waiting {
		t.Errorf("chat.inboxCap = saved %q, running %q, waiting %v; want 50, 50, false", saved, running, waiting)
	}
	for _, x := range s.Settings {
		if x.Key == "media.ghToken" && (!x.Secret || !x.Set || x.Saved != nil || x.Running != nil) {
			t.Errorf("media.ghToken = %+v, want secret, set, no values", x)
		}
	}
	if !s.Server || s.Waiting != 0 || s.Base["env"] == "" {
		t.Errorf("server %v, waiting %d, base %v", s.Server, s.Waiting, s.Base)
	}
}

type fieldErrors struct {
	Errors []struct{ Field, Message string }
}

func (p *panel) refused(w *httptest.ResponseRecorder, code int, field, message string) {
	p.t.Helper()
	var e fieldErrors
	json.Unmarshal(w.Body.Bytes(), &e)
	if w.Code != code || len(e.Errors) != 1 || e.Errors[0].Field != field || e.Errors[0].Message != message {
		p.t.Fatalf("got %d %s, want %d {%s: %s}", w.Code, w.Body, code, field, message)
	}
}

func TestOutOfRangePatchIsRefusedAndTheFileIsUntouched(t *testing.T) {
	p := envPanel(t)
	base := p.state().Base

	w := p.do("PATCH", "/api/settings/fetch.every", map[string]any{"value": "2", "base": base})

	p.refused(w, http.StatusBadRequest, "fetch.every", "Use a whole number from 3 to 1,440.")
	if got := p.read("thefeed.env"); got != envFile {
		t.Fatalf("thefeed.env changed:\n%s", got)
	}
}

// actions is the action record, newest first.
func (p *panel) actions() []admin.Action {
	p.t.Helper()
	w := p.do("GET", "/api/actions", nil)
	var out struct{ Actions []admin.Action }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		p.t.Fatalf("%v in %s", err, w.Body)
	}
	return out.Actions
}

func TestValidPatchChangesOnlyThatLine(t *testing.T) {
	p := envPanel(t)

	p.decode(p.do("PATCH", "/api/settings/fetch.every", map[string]any{"value": "20", "base": p.state().Base}), http.StatusOK)

	want := `# thefeed-server settings, written by install.sh
THEFEED_DOMAIN=t.example.com
THEFEED_FETCH_INTERVAL=20
THEFEED_GITHUB_RELAY_TOKEN=` + token + `
# hand-added, not a thefeed setting
MY_NOTE=keep
`
	if got := p.read("thefeed.env"); got != want {
		t.Fatalf("thefeed.env =\n%s\nwant\n%s", got, want)
	}
	if a := p.actions(); len(a) != 1 || a[0].Action != "Set fetch.every to 20" || a[0].Outcome != "Saved, applies after a restart" || !a[0].At.Equal(p.now) {
		t.Fatalf("action record = %+v", a)
	}
}

func TestASettingWaitsUntilTheServerRunsIt(t *testing.T) {
	p := envPanel(t)

	s := p.decode(p.do("PATCH", "/api/settings/fetch.every", map[string]any{"value": "20", "base": p.state().Base}), http.StatusOK)
	if saved, running, waiting := s.setting(t, "fetch.every"); saved != "20" || running != "10" || !waiting || s.Waiting != 1 {
		t.Fatalf("after the patch: saved %q, running %q, waiting %v, %d waiting; want 20, 10, true, 1", saved, running, waiting, s.Waiting)
	}

	p.srv.config["fetch.every"] = "20" // restarted
	s = p.state()
	if saved, running, waiting := s.setting(t, "fetch.every"); saved != "20" || running != "20" || waiting || s.Waiting != 0 {
		t.Fatalf("after the restart: saved %q, running %q, waiting %v, %d waiting; want 20, 20, false, 0", saved, running, waiting, s.Waiting)
	}
}

func TestWithTheServerStoppedSettingsShowSavedValuesOnly(t *testing.T) {
	p := envPanel(t)
	p.decode(p.do("PATCH", "/api/settings/fetch.every", map[string]any{"value": "20", "base": p.state().Base}), http.StatusOK)
	p.srv.down = true

	s := p.state()

	if saved, running, waiting := s.setting(t, "fetch.every"); saved != "20" || running != "" || waiting {
		t.Errorf("fetch.every = saved %q, running %q, waiting %v; want 20, none, false", saved, running, waiting)
	}
	if s.Server || s.Waiting != 0 {
		t.Errorf("server %v, %d waiting; want false, 0", s.Server, s.Waiting)
	}
}

func TestSettingsThePanelDoesntShowNeverWait(t *testing.T) {
	p := envPanel(t)
	p.srv.config["server.dataDir"] = "/opt/thefeed/data" // the unit's --data-dir

	if s := p.state(); s.Waiting != 0 {
		t.Fatalf("%d waiting, want 0", s.Waiting)
	}
	p.decode(p.do("POST", "/api/settings/discard", map[string]any{"base": p.state().Base}), http.StatusOK)
	if got := p.read("thefeed.env"); got != envFile {
		t.Fatalf("thefeed.env =\n%s\nwant it untouched", got)
	}
}

func TestEquivalentValuesDontWait(t *testing.T) {
	p := newPanel(t, map[string]string{"thefeed.env": "THEFEED_CHAT_ENABLED=yes\nTHEFEED_X_RSS_INSTANCES=\nTHEFEED_MSG_LIMIT=015\n"})
	p.srv.config["fetch.msgs"] = "15"

	if s := p.state(); s.Waiting != 0 {
		t.Fatalf("%d waiting, want 0: %+v", s.Waiting, s.Settings)
	}
	// The server reports no instances as "", and reads X through its two defaults.
	p.write("thefeed.env", "THEFEED_X_RSS_INSTANCES=https://nitter.net,http://nitter.net\n", started.Add(-time.Hour))
	if s := p.state(); s.Waiting != 0 {
		t.Fatalf("%d waiting, want 0 with the default instances spelled out", s.Waiting)
	}
}

func TestAnEditOnDiskAfterReadingIsNotOverwritten(t *testing.T) {
	p := envPanel(t)
	base := p.state().Base
	byHand := envFile + "THEFEED_PADDING=64\n"
	p.write("thefeed.env", byHand, time.Now())

	w := p.do("PATCH", "/api/settings/fetch.every", map[string]any{"value": "20", "base": base})

	p.refused(w, http.StatusConflict, "", "Changed elsewhere since you opened this page. Reload and try again.")
	if got := p.read("thefeed.env"); got != byHand {
		t.Fatalf("thefeed.env =\n%s\nwant the hand edit kept", got)
	}
	if a := p.actions(); len(a) != 0 {
		t.Fatalf("action record = %+v, want nothing", a)
	}
}

func TestOnlyPanelSettingsCanBePatched(t *testing.T) {
	p := envPanel(t)
	base := p.state().Base

	for key, want := range map[string]string{
		"media.ghToken":        "Replace the GitHub token with its own form.",
		"server.key":           "This setting can't be changed from the panel.",
		"server.controlSocket": "This setting can't be changed from the panel.",
		"no.such":              `There's no setting called "no.such".`,
	} {
		w := p.do("PATCH", "/api/settings/"+key, map[string]any{"value": "ghp_newTokenNewTokenNewToken123456", "base": base})
		code := http.StatusBadRequest
		if key == "no.such" {
			code = http.StatusNotFound
		}
		p.refused(w, code, key, want)
	}
	if got := p.read("thefeed.env"); got != envFile {
		t.Fatalf("thefeed.env changed:\n%s", got)
	}
}

const (
	channelsFile = "# Telegram channel usernames (one per line)\n@daily_brief\n@world_desk\n"
	privateFile  = "# Telegram private-channel invite links (one per line)\nhttps://t.me/+Xk2LqP0aZ7c4Hn\nhttps://t.me/+Rt9PwM3vB8sQ1e\n"
	xFile        = "# X usernames (one per line, without @)\nspace_daily\ntech_wire\n"
)

// sourcesPanel is a panel with every list, logged in to Telegram, and the
// server running the lists as saved.
func sourcesPanel(t *testing.T) *panel {
	p := newPanel(t, map[string]string{"thefeed.env": envFile, "channels.txt": channelsFile,
		"private_channels.txt": privateFile, "x_accounts.txt": xFile, "session.json": `{"session":"saved"}`})
	p.srv.config["dns.domain"] = "t.example.com"
	p.srv.lists = controlapi.Lists{Channels: []string{"daily_brief", "world_desk"}, Private: []string{"Xk2LqP0aZ7c4Hn", "Rt9PwM3vB8sQ1e"},
		X: []string{"space_daily", "tech_wire"}}
	return p
}

// names is a list as "name=channel" pairs, "*" marking one that waits.
func (s state) names(list string) string {
	var out []string
	for _, it := range s.Lists[list].Items {
		n := it.Name
		if it.Channel != 0 {
			n += "=" + strconv.Itoa(it.Channel)
		}
		if it.Waiting {
			n += "*"
		}
		out = append(out, n)
	}
	return strings.Join(out, " ")
}

func (p *panel) list(list string, body map[string]any) *httptest.ResponseRecorder {
	p.t.Helper()
	body["base"] = p.state().Base
	return p.do("POST", "/api/lists/"+list, body)
}

func TestAddingAPublicChannelWritesTheFileAndReloads(t *testing.T) {
	p := sourcesPanel(t)

	s := p.decode(p.list("channels", map[string]any{"op": "add", "value": "https://t.me/tech_digest"}), http.StatusOK)

	if got, want := p.read("channels.txt"), channelsFile+"@tech_digest\n"; got != want {
		t.Fatalf("channels.txt =\n%s\nwant\n%s", got, want)
	}
	if p.srv.reloads != 1 || !s.Live || s.List != "channels" {
		t.Fatalf("reloads %d, live %v, list %q; want 1, true, channels", p.srv.reloads, s.Live, s.List)
	}
	// The private channel and the X accounts after it each move up one number.
	if got := s.names("channels"); got != "daily_brief=1 world_desk=2 tech_digest=3" {
		t.Errorf("channels = %s", got)
	}
	if got := s.names("private"); got != "https://t.me/+Xk2LqP0aZ7c4Hn=4 https://t.me/+Rt9PwM3vB8sQ1e=5" {
		t.Errorf("private = %s", got)
	}
	if got := s.names("x"); got != "space_daily=6 tech_wire=7" {
		t.Errorf("x = %s", got)
	}
	if s.Waiting != 0 {
		t.Errorf("%d waiting, want 0: public channels apply live", s.Waiting)
	}
	if a := p.actions(); len(a) != 1 || a[0].Action != "Added @tech_digest to public channels" || a[0].Outcome != "Applied live" {
		t.Fatalf("action record = %+v", a)
	}
}

func TestBadAndDuplicateChannelsAreRefused(t *testing.T) {
	p := sourcesPanel(t)

	p.refused(p.list("channels", map[string]any{"op": "add", "value": "abc"}), http.StatusBadRequest, "value",
		"Channel names are 5 to 32 letters, digits or underscores, starting with a letter.")
	p.refused(p.list("channels", map[string]any{"op": "add", "value": "@Daily_Brief"}), http.StatusBadRequest, "value",
		"@Daily_Brief is already on the list.")

	if got := p.read("channels.txt"); got != channelsFile || p.srv.reloads != 0 {
		t.Fatalf("channels.txt =\n%s\nreloads %d; want it untouched, no reload", got, p.srv.reloads)
	}
}

func TestAnInviteLinkAddedAsAChannelGoesToPrivateChannelsAndWaits(t *testing.T) {
	p := sourcesPanel(t)

	s := p.decode(p.list("channels", map[string]any{"op": "add", "value": "t.me/joinchat/Ab3dEf6hIj9kLm"}), http.StatusOK)

	if got, want := p.read("private_channels.txt"), privateFile+"https://t.me/+Ab3dEf6hIj9kLm\n"; got != want {
		t.Fatalf("private_channels.txt =\n%s\nwant\n%s", got, want)
	}
	if got := p.read("channels.txt"); got != channelsFile || p.srv.reloads != 0 {
		t.Fatalf("channels.txt =\n%s\nreloads %d; want it untouched, no reload", got, p.srv.reloads)
	}
	if s.List != "private" || s.Live {
		t.Fatalf("list %q, live %v; want private, false", s.List, s.Live)
	}
	if got := s.names("private"); got != "https://t.me/+Xk2LqP0aZ7c4Hn=3 https://t.me/+Rt9PwM3vB8sQ1e=4 https://t.me/+Ab3dEf6hIj9kLm=5*" {
		t.Errorf("private = %s", got)
	}
	if !s.Lists["private"].Waiting || s.Waiting != 1 {
		t.Errorf("private waiting %v, %d waiting; want true, 1", s.Lists["private"].Waiting, s.Waiting)
	}
	if a := p.actions(); len(a) != 1 || a[0].Action != "Added an invite link to private channels" || a[0].Outcome != "Saved, applies after a restart" {
		t.Fatalf("action record = %+v", a)
	}
}

func TestPrivateChannelsNeedTheTelegramLogin(t *testing.T) {
	p := sourcesPanel(t)
	if err := os.Remove(filepath.Join(p.dir, "session.json")); err != nil {
		t.Fatal(err)
	}

	p.refused(p.list("channels", map[string]any{"op": "add", "value": "https://t.me/+Ab3dEf6hIj9kLm"}), http.StatusBadRequest, "value",
		"Private channels need the Telegram login, which is logged out.")
	p.refused(p.list("private", map[string]any{"op": "add", "value": "tg://join?invite=Ab3dEf6hIj9kLm"}), http.StatusBadRequest, "value",
		"Private channels need the Telegram login, which is logged out.")
	if got := p.read("private_channels.txt"); got != privateFile {
		t.Fatalf("private_channels.txt =\n%s\nwant it untouched", got)
	}
}

func TestRenameMoveAndRemoveWorkForEveryList(t *testing.T) {
	const (
		chHead = "# Telegram channel usernames (one per line)\n"
		pvHead = "# Telegram private-channel invite links (one per line)\n"
		xHead  = "# X usernames (one per line, without @)\n"
	)
	for _, c := range []struct {
		list       string
		body       map[string]any
		file, want string
		action     string
	}{
		{"channels", map[string]any{"op": "rename", "index": 1, "value": "t.me/world_news"}, "channels.txt", chHead + "@daily_brief\n@world_news\n",
			"Renamed @world_desk to @world_news in public channels"},
		{"channels", map[string]any{"op": "move", "index": 1, "to": 0}, "channels.txt", chHead + "@world_desk\n@daily_brief\n",
			"Moved @world_desk to place 1 in public channels"},
		{"channels", map[string]any{"op": "remove", "index": 0}, "channels.txt", chHead + "@world_desk\n",
			"Removed @daily_brief from public channels"},
		{"private", map[string]any{"op": "rename", "index": 0, "value": "+Ab3dEf6hIj9kLm"}, "private_channels.txt",
			pvHead + "https://t.me/+Ab3dEf6hIj9kLm\nhttps://t.me/+Rt9PwM3vB8sQ1e\n", "Renamed an invite link to an invite link in private channels"},
		{"private", map[string]any{"op": "move", "index": 0, "to": 1}, "private_channels.txt",
			pvHead + "https://t.me/+Rt9PwM3vB8sQ1e\nhttps://t.me/+Xk2LqP0aZ7c4Hn\n", "Moved an invite link to place 2 in private channels"},
		{"private", map[string]any{"op": "remove", "index": 1}, "private_channels.txt", pvHead + "https://t.me/+Xk2LqP0aZ7c4Hn\n",
			"Removed an invite link from private channels"},
		{"x", map[string]any{"op": "rename", "index": 0, "value": "https://x.com/space_weekly"}, "x_accounts.txt", xHead + "space_weekly\ntech_wire\n",
			"Renamed @space_daily to @space_weekly in X accounts"},
		{"x", map[string]any{"op": "move", "index": 0, "to": 1}, "x_accounts.txt", xHead + "tech_wire\nspace_daily\n",
			"Moved @space_daily to place 2 in X accounts"},
		{"x", map[string]any{"op": "remove", "index": 1}, "x_accounts.txt", xHead + "space_daily\n",
			"Removed @tech_wire from X accounts"},
		// No instances in thefeed.env: the list is the server's two defaults.
		{"instances", map[string]any{"op": "rename", "index": 1, "value": "xcancel.com"}, "thefeed.env",
			envFile + "THEFEED_X_RSS_INSTANCES=https://nitter.net,https://xcancel.com\n", "Renamed http://nitter.net to https://xcancel.com in Nitter instances"},
		{"instances", map[string]any{"op": "move", "index": 1, "to": 0}, "thefeed.env",
			envFile + "THEFEED_X_RSS_INSTANCES=http://nitter.net,https://nitter.net\n", "Moved http://nitter.net to place 1 in Nitter instances"},
		{"instances", map[string]any{"op": "remove", "index": 0}, "thefeed.env",
			envFile + "THEFEED_X_RSS_INSTANCES=http://nitter.net\n", "Removed https://nitter.net from Nitter instances"},
	} {
		t.Run(c.list+"/"+c.body["op"].(string), func(t *testing.T) {
			p := sourcesPanel(t)

			s := p.decode(p.list(c.list, c.body), http.StatusOK)

			if got := p.read(c.file); got != c.want {
				t.Errorf("%s =\n%s\nwant\n%s", c.file, got, c.want)
			}
			if live := c.list == "channels"; s.Live != live || p.srv.reloads != map[bool]int{true: 1}[live] {
				t.Errorf("live %v, %d reloads", s.Live, p.srv.reloads)
			}
			if a := p.actions(); len(a) != 1 || a[0].Action != c.action {
				t.Errorf("action record = %+v, want %q", a, c.action)
			}
		})
	}
}

func TestTheLastNitterInstanceCantBeRemoved(t *testing.T) {
	p := sourcesPanel(t)
	p.decode(p.list("instances", map[string]any{"op": "remove", "index": 0}), http.StatusOK)

	p.refused(p.list("instances", map[string]any{"op": "remove", "index": 0}), http.StatusBadRequest, "index",
		"Keep at least one instance: X is read through them.")
	if got, want := p.read("thefeed.env"), envFile+"THEFEED_X_RSS_INSTANCES=http://nitter.net\n"; got != want {
		t.Fatalf("thefeed.env =\n%s\nwant\n%s", got, want)
	}
}

func TestDiscardPutsTheFilesBackToWhatTheServerRuns(t *testing.T) {
	p := sourcesPanel(t)
	p.decode(p.do("PATCH", "/api/settings/fetch.every", map[string]any{"value": "20", "base": p.state().Base}), http.StatusOK)
	p.decode(p.list("x", map[string]any{"op": "add", "value": "@city_updates"}), http.StatusOK)
	p.decode(p.list("instances", map[string]any{"op": "add", "value": "xcancel.com"}), http.StatusOK)
	if s := p.state(); s.Waiting != 3 {
		t.Fatalf("%d waiting before the discard, want 3", s.Waiting)
	}

	s := p.decode(p.do("POST", "/api/settings/discard", map[string]any{"base": p.state().Base}), http.StatusOK)

	// thefeed.env has no delete: the instances go back to unset, as the server runs them.
	if got, want := p.read("thefeed.env"), envFile+"THEFEED_X_RSS_INSTANCES=\n"; got != want {
		t.Errorf("thefeed.env =\n%s\nwant\n%s", got, want)
	}
	if got := p.read("x_accounts.txt"); got != xFile {
		t.Errorf("x_accounts.txt =\n%s\nwant\n%s", got, xFile)
	}
	if s.Waiting != 0 {
		t.Errorf("%d waiting after the discard, want 0", s.Waiting)
	}
	if a := p.actions(); len(a) != 4 || a[0].Action != "Discarded 3 waiting changes" {
		t.Errorf("action record = %+v", a)
	}
}

func TestAListEditedByHandWaitsUntilTheServerRunsIt(t *testing.T) {
	p := sourcesPanel(t)
	p.write("x_accounts.txt", xFile+"by_hand\n", time.Now())

	s := p.state()

	if !s.Lists["x"].Waiting || s.Waiting != 1 || s.names("x") != "space_daily=5 tech_wire=6 by_hand=7*" {
		t.Fatalf("x %s, waiting %v, %d waiting; want by_hand waiting", s.names("x"), s.Lists["x"].Waiting, s.Waiting)
	}

	p.srv.lists.X = []string{"space_daily", "tech_wire", "by_hand"} // restarted
	if s := p.state(); s.Lists["x"].Waiting || s.Waiting != 0 {
		t.Errorf("x waiting %v, %d waiting after the server runs it; want false, 0", s.Lists["x"].Waiting, s.Waiting)
	}
}

func TestAChannelTheServerDidntReloadWaits(t *testing.T) {
	p := sourcesPanel(t)
	p.srv.reloadErr = errors.New("POST /channels/reload: 500 Internal Server Error: read channels: permission denied")

	s := p.decode(p.list("channels", map[string]any{"op": "add", "value": "tech_digest"}), http.StatusOK)

	if s.Live || s.ReloadError == "" {
		t.Errorf("live %v, reload error %q; want false and the error", s.Live, s.ReloadError)
	}
	if got := s.names("channels"); got != "daily_brief=1 world_desk=2 tech_digest=3*" {
		t.Errorf("channels = %s", got)
	}
	if !s.Lists["channels"].Waiting || s.Waiting != 1 {
		t.Errorf("channels waiting %v, %d waiting; want true, 1", s.Lists["channels"].Waiting, s.Waiting)
	}
}

// The server runs invite hashes: a link written another way, or one it
// can't read, doesn't wait.
func TestPrivateChannelsCompareByInviteHash(t *testing.T) {
	p := sourcesPanel(t)
	p.write("private_channels.txt", "tg://join?invite=Xk2LqP0aZ7c4Hn\nnot an invite\nt.me/joinchat/Rt9PwM3vB8sQ1e\n+Rt9PwM3vB8sQ1e\n", time.Now())

	if s := p.state(); s.Lists["private"].Waiting || s.Waiting != 0 {
		t.Errorf("private %s, waiting %v, %d waiting; want nothing waiting", s.names("private"), s.Lists["private"].Waiting, s.Waiting)
	}
}

func TestTheGitHubTokenIsReplacedButNeverEchoed(t *testing.T) {
	p := envPanel(t)
	const newToken = "github_pat_11ABCDEFG0123456789_abcdefghijklmnop"

	p.refused(p.do("POST", "/api/settings/github-token", map[string]any{"value": "hunter2", "base": p.state().Base}), http.StatusBadRequest,
		"media.ghToken", "That doesn't look like a GitHub token. Tokens start with ghp_ or github_pat_.")
	if got := p.read("thefeed.env"); got != envFile {
		t.Fatalf("thefeed.env changed:\n%s", got)
	}

	w := p.do("POST", "/api/settings/github-token", map[string]any{"value": newToken, "base": p.state().Base})

	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), newToken) || strings.Contains(w.Body.String(), token) {
		t.Fatalf("got %d, with a token in it?\n%s", w.Code, w.Body)
	}
	if got, want := p.read("thefeed.env"), strings.Replace(envFile, token, newToken, 1); got != want {
		t.Fatalf("thefeed.env =\n%s\nwant\n%s", got, want)
	}
	a := p.actions()
	if len(a) != 1 || a[0].Action != "Replaced the GitHub token" || a[0].Outcome != "Saved, applies after a restart" {
		t.Fatalf("action record = %+v", a)
	}
}

func TestTelegramLogoutTurnsTheLoginOffAndDeletesTheSession(t *testing.T) {
	p := sourcesPanel(t)
	if !p.state().Telegram.LoggedIn {
		t.Fatal("logged out before the logout")
	}

	s := p.decode(p.do("POST", "/api/settings/telegram-logout", map[string]any{"base": p.state().Base}), http.StatusOK)

	if !s.RestartNeeded || s.Telegram.LoggedIn {
		t.Errorf("restartNeeded %v, loggedIn %v; want true, false", s.RestartNeeded, s.Telegram.LoggedIn)
	}
	if got, want := p.read("thefeed.env"), envFile+"THEFEED_NO_TELEGRAM=true\n"; got != want {
		t.Errorf("thefeed.env =\n%s\nwant\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "session.json")); !os.IsNotExist(err) {
		t.Errorf("session.json: %v, want it gone", err)
	}
	if a := p.actions(); len(a) != 1 || a[0].Action != "Logged out of Telegram" || a[0].Outcome != "Session deleted, restart needed" {
		t.Fatalf("action record = %+v", a)
	}
}

type domain struct {
	Domain, Serves string
	Main           bool
	NS             []string
	PointsHere     bool
	Error          string
}

func TestDomainsAreListedWithWhatEachServes(t *testing.T) {
	p := newPanel(t, map[string]string{"thefeed.env": "THEFEED_DOMAIN=t.example.com\n"})
	for k, v := range map[string]string{"dns.domain": "t.example.com", "dns.extraDomains": "t2.example.com,t3.example.com",
		"chat.domains": "c.example.com", "mt.domains": "m.example.com"} {
		p.srv.config[k] = v
	}

	w := p.do("GET", "/api/domains", nil)
	var got struct{ Domains []domain }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var rows []string
	for _, d := range got.Domains {
		rows = append(rows, d.Domain+" "+d.Serves+map[bool]string{true: " main"}[d.Main])
	}
	if s := strings.Join(rows, ", "); s != "t.example.com feed main, t2.example.com feed, t3.example.com feed, c.example.com chat, m.example.com mtproto" {
		t.Fatalf("domains = %s", s)
	}
}

func TestTheNSCheckSaysWhichDomainsPointHere(t *testing.T) {
	p := newPanel(t, map[string]string{"thefeed.env": "THEFEED_DOMAIN=t.example.com\n"})
	for k, v := range map[string]string{"dns.domain": "t.example.com", "chat.domains": "c.example.com", "mt.domains": "m.example.com"} {
		p.srv.config[k] = v
	}
	p.dns.ns = map[string][]string{"t.example.com": {"ns1.example.com"}, "c.example.com": {"ns.elsewhere.net"}}
	p.dns.addrs = map[string][]string{"ns1.example.com": {"203.0.113.7"}, "ns.elsewhere.net": {"198.51.100.9"}}
	p.dns.here = []string{"127.0.0.1", "203.0.113.7"}

	w := p.do("POST", "/api/domains/check", map[string]any{})
	var got struct {
		CheckedAt time.Time
		Domains   []domain
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}

	if !got.CheckedAt.Equal(p.now) || len(got.Domains) != 3 {
		t.Fatalf("checked at %v, %d domains", got.CheckedAt, len(got.Domains))
	}
	if d := got.Domains[0]; d.Domain != "t.example.com" || !d.PointsHere || strings.Join(d.NS, ",") != "ns1.example.com" || d.Error != "" {
		t.Errorf("t.example.com = %+v, want it pointing here through ns1.example.com", d)
	}
	if d := got.Domains[1]; d.Domain != "c.example.com" || d.PointsHere || strings.Join(d.NS, ",") != "ns.elsewhere.net" {
		t.Errorf("c.example.com = %+v, want ns.elsewhere.net, not here", d)
	}
	if d := got.Domains[2]; d.Domain != "m.example.com" || d.PointsHere || d.Error != "lookup m.example.com: no such host" {
		t.Errorf("m.example.com = %+v, want the lookup error", d)
	}
}
