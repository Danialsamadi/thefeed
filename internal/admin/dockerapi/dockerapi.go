// Package dockerapi is a small client for the Docker Engine API: net/http
// over the Engine's unix socket, in the Engine's own API version.
// It makes only the calls the panel needs: container inspect, start, stop,
// restart, update, create, remove and logs, and image pull.
//
// An Engine error is an *Error with the Engine's own message.
package dockerapi

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// version is empty: an unversioned path speaks the Engine's own API version.
// No one version works everywhere: Engine 29 refuses anything before v1.44,
// and Engines before 25.0 (Ubuntu 22.04's and Debian 12's docker.io) refuse
// v1.44. The client reads only fields every version since v1.41 has, and
// drops the short-ID alias that v1.44 and later add.
//
// ponytail: unversioned; negotiate with GET /version if a field ever changes.
const version = ""

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
		Tty   bool
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
// ponytail: the config keeps the env, entrypoint and command the old image
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
	// From API v1.44 the container's short ID is among its aliases; the new
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
// ponytail: no registry login (X-Registry-Auth), so public images only,
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

// LogLine is a line of a container's log.
type LogLine struct {
	At   time.Time // when the container wrote it
	Text string    // without its newline
}

// Logs reads the container name's log, stdout and stderr, oldest first:
// the lines of its last tail messages. sent is how many messages the
// Engine sent; fewer than tail means that's the whole log.
func (c *Client) Logs(ctx context.Context, name string, tail int) (lines []LogLine, sent int, err error) {
	ct, err := c.Inspect(ctx, name)
	if err != nil {
		return nil, 0, err
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {strconv.Itoa(tail)}}
	resp, err := c.send(ctx, "GET", "/containers/"+url.PathEscape(name)+"/logs", q, nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	// A message is the time it was written (timestamps=1), a space and the
	// line. A line over 16 KiB is several messages, all with the line's
	// time and only the last with the newline.
	//
	// ponytail: with a TTY such a line keeps its later parts' times in its
	// text; compose runs the server without one.
	open := map[byte]int{} // stream → its line still to be finished, in lines
	for ; len(body) > 0; sent++ {
		// With a TTY, messages are lines, all on stdout.
		stream, size := byte(1), bytes.IndexByte(body, '\n')+1
		if size == 0 {
			size = len(body)
		}
		// Without one, each is a frame: its stream, three zero bytes, its
		// size (big-endian), then the message.
		if !ct.Config.Tty {
			if len(body) < 8 {
				return nil, 0, io.ErrUnexpectedEOF
			}
			stream, size, body = body[0], int(binary.BigEndian.Uint32(body[4:8])), body[8:]
		}
		p := string(body[:min(size, len(body))])
		body = body[len(p):]
		ts, text, _ := strings.Cut(p, " ")
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, 0, fmt.Errorf("Docker sent %q", strings.TrimSpace(p))
		}
		i, ok := open[stream]
		if !ok {
			i = len(lines)
			lines = append(lines, LogLine{At: at})
		}
		lines[i].Text += text
		if strings.HasSuffix(text, "\n") {
			lines[i].Text = strings.TrimRight(lines[i].Text, "\r\n")
			delete(open, stream)
		} else {
			open[stream] = i
		}
	}
	return lines, sent, nil
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
