package service_test

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin/dockerapi"
	"github.com/sartoopjj/thefeed/internal/admin/service"
)

// engine plays the Docker Engine API on a unix socket, with the one
// container docker compose made for thefeed-server. Starting the container
// starts the machine's server as the version its image runs, so /status
// and the health gate work as they do for systemd.
type engine struct {
	t        *testing.T
	m        *machine
	registry map[string]string // image → the version it runs; "crash" never answers
	images   map[string]bool   // pulled
	c        map[string]any    // the container as inspect answers it; nil once removed
	ids      int
	calls    []string          // every request but inspect, as "POST /containers/thefeed-server/stop"
	created  []map[string]any  // create bodies, in order
	fail     map[string]string // call → the message the Engine answers 500 with
	env      string            // the compose project's .env
}

const image = "ghcr.io/sartoopjj/thefeed-server"

// newEngine is thefeed-server as docker compose runs it, from image tag
// version, started when m's server was, restart policy unless-stopped.
func newEngine(t *testing.T, m *machine) *engine {
	t.Helper()
	e := &engine{t: t, m: m, registry: map[string]string{image + ":" + m.running: m.running},
		images: map[string]bool{image + ":" + m.running: true}, fail: map[string]string{}}
	e.c = e.container("4f1c2a9b3d5e0a1b", image+":"+m.running)
	e.c["State"] = map[string]any{"Status": "running", "Running": true, "StartedAt": m.started.Format(time.RFC3339Nano)}

	dir, err := os.MkdirTemp("", "d") // macOS caps a socket path at 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e.env = filepath.Join(dir, ".env")
	m.write(e.env, "# docker compose settings\nTHEFEED_SERVER_IMAGE="+image+"\nTHEFEED_SERVER_TAG="+m.running+"\nTHEFEED_ADMIN_TAG=v0.41.0\n", 0o640)
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(e.handler())
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return e
}

// options points the service module at this engine and its .env.
func (e *engine) options(o *service.Options) {
	o.Docker = dockerapi.New(filepath.Join(filepath.Dir(e.env), "s"))
	o.ComposeEnv = e.env
}

// container is what compose creates, as inspect shows it.
func (e *engine) container(id, img string) map[string]any {
	var c map[string]any
	if err := json.Unmarshal([]byte(`{
		"Id": "`+id+`", "Name": "/thefeed-server", "Image": "sha256:0b7d", "Path": "thefeed-server", "Args": ["--data-dir", "/data"],
		"State": {"Status": "created", "Running": false, "StartedAt": "0001-01-01T00:00:00Z"},
		"Config": {"Hostname": "`+id[:12]+`", "User": "thefeed", "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
			"Cmd": ["--data-dir", "/data"], "Image": "`+img+`", "WorkingDir": "/data", "Entrypoint": ["thefeed-server"],
			"ExposedPorts": {"5300/udp": {}}, "Labels": {"com.docker.compose.project": "thefeed", "com.docker.compose.service": "server"}},
		"HostConfig": {"Binds": ["/opt/thefeed/data:/data:rw"], "NetworkMode": "thefeed_default",
			"PortBindings": {"5300/udp": [{"HostIp": "", "HostPort": "5300"}]}, "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0}},
		"NetworkSettings": {"Networks": {"thefeed_default": {"IPAMConfig": null, "Links": null,
			"Aliases": ["thefeed-server", "server", "`+id[:12]+`"], "MacAddress": "02:42:ac:12:00:02", "NetworkID": "9a0e",
			"EndpointID": "e1d2", "Gateway": "172.18.0.1", "IPAddress": "172.18.0.2", "IPPrefixLen": 16,
			"DNSNames": ["thefeed-server", "server", "`+id[:12]+`"]}}}
	}`), &c); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *engine) running() bool { return e.c != nil && e.c["State"].(map[string]any)["Running"].(bool) }

func (e *engine) start() {
	img := e.c["Config"].(map[string]any)["Image"].(string)
	e.c["State"] = map[string]any{"Status": "running", "Running": true, "StartedAt": e.m.now.Format(time.RFC3339Nano)}
	m := e.m
	m.active, m.answering, m.started, m.running = true, true, m.now, e.registry[img]
	if m.running == "crash" {
		m.running = ""
	}
}

func (e *engine) stop() {
	e.c["State"] = map[string]any{"Status": "exited", "Running": false, "StartedAt": e.c["State"].(map[string]any)["StartedAt"]}
	e.m.active, e.m.running = false, ""
}

func (e *engine) handler() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	msg := func(w http.ResponseWriter, status int, text string) {
		reply(w, status, map[string]string{"message": text})
	}
	// container runs fn on the container the request names, after the
	// checks the Engine makes.
	container := func(fn func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			call := r.Method + " " + r.URL.Path
			if q, _ := url.QueryUnescape(r.URL.RawQuery); q != "" {
				call += "?" + q
			}
			if r.Method != "GET" {
				e.calls = append(e.calls, call)
			}
			if m, ok := e.fail[call]; ok {
				msg(w, http.StatusInternalServerError, m)
				return
			}
			if r.Method == "POST" && r.ContentLength > 0 && r.Header.Get("Content-Type") != "application/json" {
				msg(w, http.StatusBadRequest, "unsupported Content-Type header ("+r.Header.Get("Content-Type")+"): must be 'application/json'")
				return
			}
			if name := r.PathValue("name"); name != "" && (e.c == nil || name != "thefeed-server") {
				msg(w, http.StatusNotFound, "No such container: "+name)
				return
			}
			fn(w, r)
		}
	}
	mux.HandleFunc("GET /containers/{name}/json", container(func(w http.ResponseWriter, r *http.Request) {
		reply(w, http.StatusOK, e.c)
	}))
	mux.HandleFunc("POST /containers/{name}/start", container(func(w http.ResponseWriter, r *http.Request) {
		if e.running() {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		e.start()
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /containers/{name}/stop", container(func(w http.ResponseWriter, r *http.Request) {
		if !e.running() {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		e.stop()
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /containers/{name}/restart", container(func(w http.ResponseWriter, r *http.Request) {
		e.start()
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /containers/{name}/update", container(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			msg(w, http.StatusBadRequest, err.Error())
			return
		}
		e.c["HostConfig"].(map[string]any)["RestartPolicy"] = body["RestartPolicy"]
		reply(w, http.StatusOK, map[string]any{"Warnings": []string{}})
	}))
	mux.HandleFunc("DELETE /containers/{name}", container(func(w http.ResponseWriter, r *http.Request) {
		if e.running() {
			msg(w, http.StatusConflict, `cannot remove container "/thefeed-server": container is running: stop the container before removing or force remove`)
			return
		}
		e.c = nil
		w.WriteHeader(http.StatusNoContent)
	}))
	// Pulling answers 200 and streams its progress; a failure is in the stream.
	mux.HandleFunc("POST /images/create", container(func(w http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("fromImage") + ":" + r.URL.Query().Get("tag")
		w.Header().Set("Content-Type", "application/json")
		if _, ok := e.registry[ref]; !ok {
			io.WriteString(w, `{"status":"Pulling from sartoopjj/thefeed-server"}`+"\n"+
				`{"errorDetail":{"message":"manifest unknown"},"error":"manifest unknown"}`+"\n")
			return
		}
		e.images[ref] = true
		io.WriteString(w, `{"status":"Pulling from sartoopjj/thefeed-server"}`+"\n"+`{"status":"Status: Downloaded newer image for `+ref+`"}`+"\n")
	}))
	mux.HandleFunc("POST /containers/create", container(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			msg(w, http.StatusBadRequest, err.Error())
			return
		}
		if e.c != nil {
			msg(w, http.StatusConflict, `Conflict. The container name "/thefeed-server" is already in use by container "`+e.c["Id"].(string)+`".`)
			return
		}
		img, _ := body["Image"].(string)
		if !e.images[img] {
			msg(w, http.StatusNotFound, "No such image: "+img)
			return
		}
		e.created = append(e.created, body)
		e.ids++
		id := fmt.Sprintf("c0ffee%010d", e.ids)
		cfg := maps.Clone(body)
		delete(cfg, "HostConfig")
		delete(cfg, "NetworkingConfig")
		cfg["Hostname"] = id[:12]
		nets := map[string]any{}
		for n, ep := range body["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any) {
			aliases := append(ep.(map[string]any)["Aliases"].([]any), id[:12])
			nets[n] = map[string]any{"Aliases": aliases, "NetworkID": "9a0e", "EndpointID": "e1d3", "IPAddress": "172.18.0.3"}
		}
		e.c = map[string]any{"Id": id, "Name": "/" + r.URL.Query().Get("name"), "Image": "sha256:7c1f", "Path": "thefeed-server", "Args": cfg["Cmd"],
			"State":  map[string]any{"Status": "created", "Running": false, "StartedAt": "0001-01-01T00:00:00Z"},
			"Config": cfg, "HostConfig": body["HostConfig"], "NetworkSettings": map[string]any{"Networks": nets}}
		reply(w, http.StatusCreated, map[string]any{"Id": id, "Warnings": []string{}})
	}))
	return mux
}

// dockerState is GET /api/service with Docker mode's fields.
type dockerState struct {
	state
	Server  string
	Name    string
	FixUnit bool
}

func (p *panel) dockerState(w *httptest.ResponseRecorder) dockerState {
	p.t.Helper()
	p.state(w) // wants a 200
	var s dockerState
	json.Unmarshal(w.Body.Bytes(), &s)
	return s
}

// With THEFEED_ADMIN_SERVER=docker, the state is the container's: running
// since State.StartedAt, start on boot is its restart policy, and fix-unit
// isn't on offer.
func TestDockerStateIsTheContainers(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	p := newPanel(t, m, e.options)

	s := p.dockerState(p.do("GET", "/api/service", nil))
	if s.Server != "docker" || s.Name != "thefeed-server" || s.FixUnit || s.State != "running" || !s.Started.Equal(m.started) || !s.Boot || s.Version != "v0.41.0" {
		t.Fatalf("state = %+v, want the docker container thefeed-server running v0.41.0 since %s, starting with Docker, no fix-unit", s, m.started)
	}

	e.stop() // docker stop thefeed-server, behind the panel's back
	e.c["HostConfig"].(map[string]any)["RestartPolicy"] = map[string]any{"Name": "no"}
	if s := p.dockerState(p.do("GET", "/api/service", nil)); s.State != "stopped" || s.Boot {
		t.Fatalf("after docker stop: %+v, want stopped, not starting with Docker", s)
	}

	sd := newPanel(t, newMachine(t, "v0.41.0"))
	if s := sd.dockerState(sd.do("GET", "/api/service", nil)); s.Server != "systemd" || s.Name != "thefeed-server" || !s.FixUnit {
		t.Fatalf("systemd mode: %+v, want server systemd, unit thefeed-server, fix-unit on offer", s)
	}
}

func (e *engine) wantCalls(want ...string) {
	e.t.Helper()
	if !slices.Equal(e.calls, want) {
		e.t.Fatalf("Docker got %q, want %q", e.calls, want)
	}
}

// Restart, stop and start act on the container, not on a systemd unit.
func TestDockerRestartStopAndStartTheContainer(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	p := newPanel(t, m, e.options)
	for _, c := range []struct{ action, call, state, last string }{
		{"stop", "POST /containers/thefeed-server/stop", "stopped", ""},
		{"start", "POST /containers/thefeed-server/start", "running", "Started"},
		{"restart", "POST /containers/thefeed-server/restart", "running", "Restarted"},
	} {
		e.calls = nil
		m.now = m.now.Add(time.Minute)
		s := p.act(c.action, nil)
		e.wantCalls(c.call)
		if s.State != c.state || c.last != "" && (s.Last == nil || s.Last.Text != c.last) {
			t.Errorf("after %s: %+v, want %s with last line %q", c.action, s, c.state, c.last)
		}
		if c.state == "running" && !s.Started.Equal(m.now) {
			t.Errorf("after %s: started %s, want now", c.action, s.Started)
		}
	}
	m.wantCmds()
	if got, want := p.actions(), []string{"Restart: Done", "Start server: Done", "Stop server: Done"}; !slices.Equal(got, want) {
		t.Fatalf("action record = %q, want %q", got, want)
	}
}

// The Engine's own message reaches the operator as a 502.
func TestDockerErrorsSayWhatTheEngineSaid(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	p := newPanel(t, m, e.options)
	e.fail["POST /containers/thefeed-server/restart"] = "Cannot restart container thefeed-server: driver failed programming external connectivity"
	m.latest["sartoopjj/thefeed"] = "v0.41.1"

	_, msg := p.errorOf(p.do("POST", "/api/service/restart", struct{}{}), http.StatusBadGateway)
	if want := "Cannot restart container thefeed-server: driver failed programming external connectivity"; msg != want {
		t.Errorf("restart: %q, want %q", msg, want)
	}
	if got := p.actions(); len(got) != 1 || got[0] != "Restart: Cannot restart container thefeed-server: driver failed programming external connectivity" {
		t.Errorf("action record = %q, want the failed restart", got)
	}

	// A pull answers 200 and fails inside its stream; nothing else happens.
	e.calls = nil
	env := m.read(e.env)
	_, msg = p.errorOf(p.do("POST", "/api/service/update", struct{}{}), http.StatusBadGateway)
	if want := "Couldn't pull ghcr.io/sartoopjj/thefeed-server:v0.41.1: manifest unknown"; msg != want {
		t.Errorf("update: %q, want %q", msg, want)
	}
	e.wantCalls(pull + "v0.41.1")
	if got := m.read(e.env); got != env {
		t.Errorf(".env = %q, want it untouched", got)
	}

	e.c = nil // docker rm thefeed-server
	if _, msg := p.errorOf(p.do("GET", "/api/service", nil), http.StatusBadGateway); msg != "Couldn't ask Docker about thefeed-server: No such container: thefeed-server" {
		t.Errorf("state: %q, want Docker's no such container", msg)
	}
}

// Settings in the container's environment or command beat thefeed.env. The
// panel lists them, and there's no unit to fix: they're removed from
// docker-compose.yml. A leftover unit file doesn't count.
func TestDockerShadowingIsTheComposeSettings(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	m.write(m.unit, "[Service]\nExecStart="+m.bin+" --data-dir "+m.data+" --domain t.example.com\n", 0o644)
	e := newEngine(t, m)
	cfg := e.c["Config"].(map[string]any)
	cfg["Env"] = []any{"PATH=/usr/bin", "THEFEED_DOMAIN=t.example.com", "THEFEED_DATA_DIR=/data", "THEFEED_ADMIN_SERVER=docker", "THEFEED_DEBUG=1"}
	e.c["Args"] = []any{"--data-dir", "/data", "--listen", ":5300", "-key=abc", "--verbose"}
	p := newPanel(t, m, e.options)

	s := p.dockerState(p.do("GET", "/api/service", nil))
	if want := []string{"THEFEED_DOMAIN", "THEFEED_DEBUG", "--listen", "--key"}; !slices.Equal(s.Shadowing, want) {
		t.Fatalf("shadowing = %q, want %q", s.Shadowing, want)
	}

	e.calls = nil
	field, msg := p.errorOf(p.do("POST", "/api/service/fix-unit", struct{}{}), http.StatusBadRequest)
	if field != "" || !strings.Contains(msg, "docker-compose.yml") {
		t.Errorf("fix-unit: %q %q, want a 400 saying to remove them from docker-compose.yml", field, msg)
	}
	e.wantCalls()
	m.wantCmds()
	if got := p.actions(); len(got) != 0 {
		t.Errorf("action record = %q, want nothing", got)
	}
}

// recreated is the create body for compose's container on image tag:
// the same config without its hostname, the same host config, and the
// network's aliases without the old container's ID.
func recreated(t *testing.T, tag string) map[string]any {
	t.Helper()
	var want map[string]any
	if err := json.Unmarshal([]byte(`{
		"User": "thefeed", "Env": ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"],
		"Cmd": ["--data-dir", "/data"], "Image": "ghcr.io/sartoopjj/thefeed-server:`+tag+`", "WorkingDir": "/data", "Entrypoint": ["thefeed-server"],
		"ExposedPorts": {"5300/udp": {}}, "Labels": {"com.docker.compose.project": "thefeed", "com.docker.compose.service": "server"},
		"HostConfig": {"Binds": ["/opt/thefeed/data:/data:rw"], "NetworkMode": "thefeed_default",
			"PortBindings": {"5300/udp": [{"HostIp": "", "HostPort": "5300"}]}, "RestartPolicy": {"Name": "unless-stopped", "MaximumRetryCount": 0}},
		"NetworkingConfig": {"EndpointsConfig": {"thefeed_default": {"Aliases": ["thefeed-server", "server"]}}}
	}`), &want); err != nil {
		t.Fatal(err)
	}
	return want
}

// recreate is the calls that put compose's container on another image.
var recreate = []string{"POST /containers/thefeed-server/stop", "DELETE /containers/thefeed-server",
	"POST /containers/create?name=thefeed-server", "POST /containers/thefeed-server/start"}

const pull = "POST /images/create?fromImage=ghcr.io/sartoopjj/thefeed-server&tag="

// An update pulls the release's image, writes its tag into compose's .env
// so docker compose up -d keeps it, and recreates the container on it.
func TestDockerUpdatePullsAndRecreates(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	p := newPanel(t, m, e.options)
	e.registry[image+":v0.41.1"] = "v0.41.1"
	m.latest["sartoopjj/thefeed"] = "v0.41.1"
	e.calls = nil
	m.now = m.now.Add(time.Minute)

	s := p.act("update", nil)

	e.wantCalls(slices.Concat([]string{pull + "v0.41.1"}, recreate)...)
	if len(e.created) != 1 || !reflect.DeepEqual(e.created[0], recreated(t, "v0.41.1")) {
		t.Fatalf("created %v\nwant %v", e.created, recreated(t, "v0.41.1"))
	}
	if got, want := m.read(e.env), "# docker compose settings\nTHEFEED_SERVER_IMAGE="+image+"\nTHEFEED_SERVER_TAG=v0.41.1\nTHEFEED_ADMIN_TAG=v0.41.0\n"; got != want {
		t.Errorf(".env =\n%s\nwant\n%s", got, want)
	}
	if fi, _ := os.Stat(e.env); fi.Mode().Perm() != 0o640 {
		t.Errorf(".env mode %v, want 0640 kept", fi.Mode().Perm())
	}
	m.wantCmds()
	if s.State != "running" || s.Version != "v0.41.1" || s.Previous != "v0.41.0" || s.Update || !s.Started.Equal(m.now) || s.Last == nil || s.Last.Text != "Updated to v0.41.1" {
		t.Fatalf("state = %+v, want v0.41.1 running since now with v0.41.0 to go back to", s)
	}
	if got := p.actions(); got[0] != "Update to v0.41.1: Done" {
		t.Fatalf("action record = %q, want Update to v0.41.1: Done first", got)
	}
}

// An image that doesn't answer healthy within 60 seconds is swapped back
// out: the container goes back on the image it ran, and .env to its tag.
func TestDockerUnhealthyUpdateGoesBack(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	p := newPanel(t, m, e.options)
	e.registry[image+":v0.41.1"] = "crash"
	m.latest["sartoopjj/thefeed"] = "v0.41.1"
	env := m.read(e.env)
	e.calls = nil
	start := m.now

	_, msg := p.errorOf(p.do("POST", "/api/service/update", struct{}{}), http.StatusBadGateway)

	if !strings.Contains(msg, "60 seconds") || !strings.Contains(msg, "v0.41.0") {
		t.Errorf("error = %q, want it to say v0.41.1 wasn't healthy in 60 seconds and v0.41.0 is back", msg)
	}
	if m.now.Sub(start) < time.Minute {
		t.Errorf("gave up after %s, want a minute", m.now.Sub(start))
	}
	e.wantCalls(slices.Concat([]string{pull + "v0.41.1"}, recreate, recreate)...)
	if want := []map[string]any{recreated(t, "v0.41.1"), recreated(t, "v0.41.0")}; !reflect.DeepEqual(e.created, want) {
		t.Fatalf("created %v\nwant %v", e.created, want)
	}
	if got := m.read(e.env); got != env || m.running != "v0.41.0" {
		t.Fatalf(".env %q, running %q; want .env as it was and v0.41.0 running", got, m.running)
	}
	s := p.state(p.do("GET", "/api/service", nil))
	if s.State != "running" || s.Version != "v0.41.0" || s.Previous != "" || s.Last == nil || !strings.Contains(s.Last.Text, "rolled back") {
		t.Fatalf("state = %+v, want v0.41.0 running, nothing to roll back to, and a line saying it rolled back", s)
	}
	if got := p.actions(); !strings.Contains(got[0], "Update to v0.41.1: ") || !strings.Contains(got[0], "rolled back") {
		t.Fatalf("action record = %q, want the update and its rollback first", got)
	}
}

// When even the old image can't be put back, .env still goes back to its
// tag, so docker compose up -d brings back the version that worked.
func TestDockerUpdateThatCantGoBackStillRestoresTheEnvFile(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	p := newPanel(t, m, e.options)
	e.registry[image+":v0.41.1"] = "v0.41.1"
	m.latest["sartoopjj/thefeed"] = "v0.41.1"
	e.fail["POST /containers/create?name=thefeed-server"] = "driver failed programming external connectivity: port 5300 is already allocated"
	env := m.read(e.env)

	_, msg := p.errorOf(p.do("POST", "/api/service/update", struct{}{}), http.StatusBadGateway)

	if !strings.Contains(msg, "putting v0.41.0 back failed") || !strings.Contains(msg, "port 5300 is already allocated") {
		t.Errorf("error = %q, want it to say putting v0.41.0 back failed, and why", msg)
	}
	if got := m.read(e.env); got != env {
		t.Fatalf(".env = %q, want %q back", got, env)
	}
}

// Rolling back pulls the tag the last update replaced, which an install
// that started on latest never pulled, writes it into .env and recreates
// the container on it.
func TestDockerRollBackRecreatesOnThePreviousTag(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	p := newPanel(t, m, e.options)
	p.errorOf(p.do("POST", "/api/service/rollback", struct{}{}), http.StatusConflict)
	e.registry[image+":v0.41.1"] = "v0.41.1"
	m.latest["sartoopjj/thefeed"] = "v0.41.1"
	p.act("update", nil)
	delete(e.images, image+":v0.41.0")
	e.calls = nil

	s := p.act("rollback", nil)

	e.wantCalls(slices.Concat([]string{pull + "v0.41.0"}, recreate)...)
	if len(e.created) != 2 || !reflect.DeepEqual(e.created[1], recreated(t, "v0.41.0")) {
		t.Fatalf("created %v\nwant %v last", e.created, recreated(t, "v0.41.0"))
	}
	if got, want := m.read(e.env), "# docker compose settings\nTHEFEED_SERVER_IMAGE="+image+"\nTHEFEED_SERVER_TAG=v0.41.0\nTHEFEED_ADMIN_TAG=v0.41.0\n"; got != want {
		t.Errorf(".env =\n%s\nwant\n%s", got, want)
	}
	if m.running != "v0.41.0" || s.Version != "v0.41.0" || s.Previous != "" || !s.Update || s.Last == nil || s.Last.Text != "Rolled back to v0.41.0" {
		t.Fatalf("running %q, state %+v; want v0.41.0, v0.41.1 on offer again, nothing to roll back to", m.running, s)
	}
	if got := p.actions(); got[0] != "Roll back to v0.41.0: Done" {
		t.Fatalf("action record = %q, want Roll back to v0.41.0: Done first", got)
	}
}

// Compose's own defaults: no .env, and the image tagged latest. An update
// writes a .env with the tag, and rolling back pulls the tag it replaced.
func TestDockerUpdateFromLatestWithoutAnEnvFile(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	e.c["Config"].(map[string]any)["Image"] = image + ":latest"
	e.registry[image+":latest"], e.images[image+":latest"] = "v0.41.0", true
	delete(e.images, image+":v0.41.0")
	if err := os.Remove(e.env); err != nil {
		t.Fatal(err)
	}
	p := newPanel(t, m, e.options)
	e.registry[image+":v0.41.1"] = "v0.41.1"
	m.latest["sartoopjj/thefeed"] = "v0.41.1"

	p.act("update", nil)
	if got := m.read(e.env); got != "THEFEED_SERVER_TAG=v0.41.1\n" {
		t.Fatalf(".env = %q, want THEFEED_SERVER_TAG=v0.41.1", got)
	}
	if got := e.created[0]["Image"]; got != image+":v0.41.1" {
		t.Fatalf("created on %v, want %s:v0.41.1", got, image)
	}

	e.calls = nil
	s := p.act("rollback", nil)
	e.wantCalls(slices.Concat([]string{pull + "v0.41.0"}, recreate)...)
	if got := m.read(e.env); got != "THEFEED_SERVER_TAG=v0.41.0\n" || m.running != "v0.41.0" || s.Version != "v0.41.0" {
		t.Fatalf(".env %q, running %q, state %+v; want v0.41.0 back", got, m.running, s)
	}
}

// Start on boot is the restart policy: unless-stopped, or no. Reboot and
// shut down still act on the machine, through systemctl.
func TestDockerStartOnBootIsTheRestartPolicy(t *testing.T) {
	m := newMachine(t, "v0.41.0")
	e := newEngine(t, m)
	p := newPanel(t, m, e.options)
	for _, c := range []struct {
		action, policy string
		boot           bool
	}{{"boot-off", "no", false}, {"boot-on", "unless-stopped", true}} {
		e.calls = nil
		s := p.act(c.action, nil)
		e.wantCalls("POST /containers/thefeed-server/update")
		if got, want := e.c["HostConfig"].(map[string]any)["RestartPolicy"], map[string]any{"Name": c.policy}; !reflect.DeepEqual(got, want) {
			t.Errorf("after %s: restart policy %v, want %v", c.action, got, want)
		}
		if s.Boot != c.boot || s.State != "running" {
			t.Errorf("after %s: %+v, want running, boot %v", c.action, s, c.boot)
		}
	}

	e.calls = nil
	p.act("reboot", nil)
	m.wantCmds("reboot")
	e.wantCalls()
	if got, want := p.actions(), []string{"Reboot: Done", "Start on boot on: Done", "Start on boot off: Done"}; !slices.Equal(got, want) {
		t.Fatalf("action record = %q, want %q", got, want)
	}
}
