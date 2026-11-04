// Package dockerapi is a small client for the Docker Engine API: net/http
// over the Engine's unix socket, with the API version pinned in the path.
// It makes only the calls the panel needs: container inspect, start, stop,
// restart, update, create and remove, and image pull.
//
// An Engine error is an *Error with the Engine's own message.
package dockerapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// version is the Engine API version the client speaks. v1.44 is Engine
// 25.0; Engine 29.0 to 29.2 refuse anything older.
//
// Pinned; negotiate with GET /version if an Engine past 1.44's
// support window ever turns it away.
const version = "/v1.44"

// Client talks to one Docker Engine.
type Client struct{ hc *http.Client }

// New is a client for the Engine listening on socket, such as
// /var/run/docker.sock.
func New(socket string) *Client {
	// No timeout: pulling an image can take minutes. Callers pass a context.
	return &Client{hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
}

// Error is the Engine turning a request down.
type Error struct {
	Status  int // HTTP status, such as 404 for no such container
	Message string
}

func (e *Error) Error() string { return e.Message }

// Container is what inspect says about a container.
type Container struct {
	ID    string `json:"Id"`
	Name  string
	Path  string   // the program it runs
	Args  []string // its arguments
	State struct {
		Running   bool
		StartedAt time.Time
	}
	Config struct {
		Image string   // as it was created from, such as ghcr.io/o/thefeed-server:v0.41.0
		Env   []string // KEY=value
	}
	HostConfig struct {
		RestartPolicy struct{ Name string }
	}
	raw json.RawMessage // all of it, for Create
}

// Inspect reads the container name.
func (c *Client) Inspect(ctx context.Context, name string) (*Container, error) {
	out := &Container{}
	if err := c.do(ctx, "GET", "/containers/"+url.PathEscape(name)+"/json", nil, nil, &out.raw); err != nil {
		return nil, err
	}
	return out, json.Unmarshal(out.raw, out)
}

// Start, Stop and Restart the container name. Stop and Restart give it
// Docker's stop timeout to exit before it's killed.
func (c *Client) Start(ctx context.Context, name string) error   { return c.post(ctx, name, "start") }
func (c *Client) Stop(ctx context.Context, name string) error    { return c.post(ctx, name, "stop") }
func (c *Client) Restart(ctx context.Context, name string) error { return c.post(ctx, name, "restart") }

func (c *Client) post(ctx context.Context, name, verb string) error {
	return c.do(ctx, "POST", "/containers/"+url.PathEscape(name)+"/"+verb, nil, nil, nil)
}

// SetRestart sets the container's restart policy, such as unless-stopped or no.
func (c *Client) SetRestart(ctx context.Context, name, policy string) error {
	body := map[string]any{"RestartPolicy": map[string]string{"Name": policy}}
	return c.do(ctx, "POST", "/containers/"+url.PathEscape(name)+"/update", nil, body, nil)
}

// Remove removes the stopped container name.
func (c *Client) Remove(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/containers/"+url.PathEscape(name), nil, nil, nil)
}

// Create creates a container like one inspected, under its name, on
// image: the same config without its hostname, the same host config, and
// the same networks with their aliases. The new container gets its own
// IDs and addresses.
//
// The config keeps the env, entrypoint and command the old image
// filled in, and drops a static address set on a network; the next docker
// compose up -d puts both right. Subtract the old image's config, or carry
// IPAMConfig over, if a release ever depends on either.
func (c *Client) Create(ctx context.Context, like *Container, image string) error {
	var old struct {
		Config          map[string]any
		HostConfig      json.RawMessage
		NetworkSettings struct {
			Networks map[string]struct{ Aliases []string }
		}
	}
	if err := json.Unmarshal(like.raw, &old); err != nil {
		return err
	}
	body := old.Config
	body["Image"] = image
	delete(body, "Hostname")
	body["HostConfig"] = old.HostConfig
	// API v1.44 lists the container's short ID among its aliases; the new
	// container gets its own.
	short := like.ID[:min(12, len(like.ID))]
	eps := map[string]any{}
	for name, ep := range old.NetworkSettings.Networks {
		eps[name] = map[string]any{"Aliases": slices.DeleteFunc(ep.Aliases, func(a string) bool { return a == short })}
	}
	body["NetworkingConfig"] = map[string]any{"EndpointsConfig": eps}
	q := url.Values{"name": {strings.TrimPrefix(like.Name, "/")}}
	return c.do(ctx, "POST", "/containers/create", q, body, nil)
}

// Pull pulls repo:tag. The Engine streams its progress, and a failure is
// an error in the stream.
//
// No registry login (X-Registry-Auth), so public images only,
// as the release workflow publishes them.
func (c *Client) Pull(ctx context.Context, repo, tag string) error {
	resp, err := c.send(ctx, "POST", "/images/create", url.Values{"fromImage": {repo}, "tag": {tag}}, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for d := json.NewDecoder(resp.Body); ; {
		var m struct {
			Error       string
			ErrorDetail struct{ Message string }
		}
		if err := d.Decode(&m); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if msg := cmp.Or(m.ErrorDetail.Message, m.Error); msg != "" {
			return &Error{resp.StatusCode, msg}
		}
	}
}

// do sends a request and decodes the answer into out when it isn't nil.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	resp, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil || resp.StatusCode == http.StatusNotModified {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// send sends a request with body as JSON, and returns the answer if it
// worked. 304, "already started" or "already stopped", worked.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	u := "http://docker" + version + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		defer resp.Body.Close()
		var e struct{ Message string }
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		return nil, &Error{resp.StatusCode, cmp.Or(e.Message, "Docker answered "+resp.Status)}
	}
	return resp, nil
}
