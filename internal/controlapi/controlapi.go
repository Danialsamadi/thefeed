// Package controlapi is the contract between thefeed-server and the admin
// panel: the JSON spoken on the server's local control socket, and a client
// for it.
//
// The socket is <data-dir>/control.sock, mode 0600, HTTP over a Unix socket:
//
//	GET  /status            Status
//	POST /channels/reload   re-read channels.txt and apply it live
//	POST /refresh           fetch everything now
//	POST /answering         {"on": bool}; off leaves DNS unanswered
//
// POSTs answer 204 on success. Any other status is an error whose body is
// the reason, in plain text.
//
// In Status, a feature that is off is left out (a nil pointer), so "off"
// never looks like "on with zero traffic".
package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// SocketName is the control socket's file name in the server's data directory.
const SocketName = "control.sock"

// Status is GET /status.
type Status struct {
	Server     Server           `json:"server"`
	Feed       Feed             `json:"feed"`
	Chat       *Chat            `json:"chat,omitempty"`
	MediaCache *MediaCacheStats `json:"mediaCache,omitempty"`
	GHRelay    *RelayStatus     `json:"ghRelay,omitempty"`
	MTProto    *MTProto         `json:"mtproto,omitempty"`
}

// Server is the running process.
type Server struct {
	Version   string    `json:"version"`
	Started   time.Time `json:"started"`
	Answering bool      `json:"answering"` // false while DNS answering is paused
	// Config is every setting in the serverconfig registry, by key, with the
	// value the server is running with, as thefeed.env would write it.
	// Secret settings are left out.
	Config map[string]string `json:"config"`
}

// Feed is DNS query volume, per domain the server answers on: feed, chat
// and tunnel domains alike.
type Feed struct {
	Domains []DomainQueries `json:"domains"`
}

// DomainQueries is one domain's queries.
type DomainQueries struct {
	Domain    string `json:"domain"`
	Queries   int64  `json:"queries"`   // since the server started
	PerMinute int64  `json:"perMinute"` // in the last 60 seconds
}

// Chat is the messenger. "Today" is the current UTC day.
type Chat struct {
	ActiveToday    int   `json:"activeToday"`    // addresses that used the messenger
	MessagesToday  int64 `json:"messagesToday"`  // messages accepted
	Waiting        int   `json:"waiting"`        // stored, not yet picked up
	InboxFullToday int64 `json:"inboxFullToday"` // sends refused because the inbox was full
	StoreErrors    int64 `json:"storeErrors"`    // message store errors in the last 10 minutes
}

// MTProto is the Telegram-over-DNS tunnel.
type MTProto struct {
	Sessions    int `json:"sessions"`    // users connected; the cap counts these
	LiveTunnels int `json:"liveTunnels"` // sockets open to Telegram right now
	MaxTunnels  int `json:"maxTunnels"`  // 0 means uncapped
}

// MediaCacheStats is the media cache's counters.
type MediaCacheStats struct {
	Entries int64 `json:"entries"`
	// Bytes is the logical size of cached files, including relay-only ones
	// whose content is NOT in memory. RAMBytes is the heap actually held —
	// use that one when diagnosing memory.
	Bytes         int64  `json:"bytes"`
	RAMBytes      int64  `json:"ramBytes"`
	Queries       uint64 `json:"queries"`
	StoreHits     uint64 `json:"storeHits"`
	StoreMisses   uint64 `json:"storeMisses"`
	StoreRejected uint64 `json:"storeRejected"`
	Evictions     uint64 `json:"evictions"`
	MaxFileBytes  int64  `json:"maxFileBytes"`
	TTLSeconds    int64  `json:"ttlSeconds"`
}

// RelayStatus is the GitHub relay's health.
type RelayStatus struct {
	Repo         string  `json:"repo"`
	RepoSizeKB   int64   `json:"repoSizeKB"`
	QuotaKB      int64   `json:"quotaKB"`
	PercentUsed  float64 `json:"percentUsed"`
	PendingFiles int     `json:"pendingFiles"`
	KnownObjects int     `json:"knownObjects"`
	Quota403     bool    `json:"quotaExhausted"`
	TokenDenied  bool    `json:"tokenDenied"`
	FailStreak   int     `json:"failStreak"`
	LastError    string  `json:"lastError,omitempty"`
}

// Answering is the body of POST /answering. On is required.
type Answering struct {
	On *bool `json:"on"`
}

// Client is what the admin panel needs from the running server. New returns
// the real one; the panel's tests fake it.
type Client interface {
	Status(ctx context.Context) (*Status, error)
	ReloadChannels(ctx context.Context) error
	Refresh(ctx context.Context) error
	SetAnswering(ctx context.Context, on bool) error
}

// New returns a Client for the control socket at socketPath.
func New(socketPath string) Client {
	return &client{hc: &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		}},
	}}
}

type client struct{ hc *http.Client }

func (c *client) Status(ctx context.Context) (*Status, error) {
	var st Status
	if err := c.do(ctx, http.MethodGet, "/status", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (c *client) ReloadChannels(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/channels/reload", nil, nil)
}

func (c *client) Refresh(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/refresh", nil, nil)
}

func (c *client) SetAnswering(ctx context.Context, on bool) error {
	return c.do(ctx, http.MethodPost, "/answering", Answering{On: &on}, nil)
}

func (c *client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	// The host is ignored: the transport always dials the socket.
	req, err := http.NewRequestWithContext(ctx, method, "http://thefeed-server"+path, body)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(msg))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
