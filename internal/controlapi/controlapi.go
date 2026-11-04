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
//	POST /checks/{feature}  run that feature's check now; CheckResult
//
// POSTs answer 204 on success, except checks, which answer 200 with a
// CheckResult whether or not the feature passed. Any other status is an
// error whose body is the reason, in plain text.
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
	"net/url"
	"time"
)

// SocketName is the control socket's file name in the server's data directory.
const SocketName = "control.sock"

// Status is GET /status.
type Status struct {
	Server     Server           `json:"server"`
	Lists      Lists            `json:"lists"`
	Feed       Feed             `json:"feed"`
	Channels   *Channels        `json:"channels,omitempty"`
	Chat       *Chat            `json:"chat,omitempty"`
	MediaCache *MediaCacheStats `json:"mediaCache,omitempty"`
	GHRelay    *RelayStatus     `json:"ghRelay,omitempty"`
	MTProto    *MTProto         `json:"mtproto,omitempty"`
	X          *X               `json:"x,omitempty"`
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

// Lists is the source lists the server runs, never null: public channels as
// last loaded (at start or by a reload), and the private invite hashes and X
// accounts it read at start. The Nitter instances are in Server.Config.
type Lists struct {
	Channels []string `json:"channels"` // names, without the @
	Private  []string `json:"private"`  // invite hashes
	X        []string `json:"x"`
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
	Sessions    int  `json:"sessions"`    // users connected; the cap counts these
	LiveTunnels int  `json:"liveTunnels"` // sockets open to Telegram right now
	MaxTunnels  int  `json:"maxTunnels"`  // 0 means uncapped
	DCs         []DC `json:"dcs"`         // every data centre the relay dials, by id
}

// DC is one Telegram data centre's dials, from tunnels and checks.
type DC struct {
	ID               int       `json:"id"`
	OK               bool      `json:"ok"`              // the last dial connected
	Error            string    `json:"error,omitempty"` // the last failed dial's, kept after good ones
	DialMs           int64     `json:"dialMs"`          // how long the last dial took
	At               time.Time `json:"at,omitzero"`     // the last dial; zero: never dialled
	FailuresLastHour int       `json:"failuresLastHour"`
}

// Channels is the Telegram channel reader, logged in or public.
type Channels struct {
	IntervalMs int64        `json:"intervalMs"` // between automatic cycles
	Cycles     []FetchCycle `json:"cycles"`     // the last 10, oldest first
}

// FetchCycle is one pass over every channel.
type FetchCycle struct {
	Start      time.Time `json:"start"`
	DurationMs int64     `json:"durationMs"`
	// Fetched is the channels whose posts are fresh after the cycle: read
	// now, or skipped because the cache was still fresh. Total less Fetched
	// is the channels that failed.
	Fetched int `json:"fetched"`
	Total   int `json:"total"`
	Posts   int `json:"posts"` // read this cycle
}

// X is the X reader, which reads accounts through Nitter instances.
type X struct {
	Accounts int `json:"accounts"`
	// LastPost is when the reader last got an account's posts.
	LastPost  time.Time  `json:"lastPost,omitzero"`
	Instances []Instance `json:"instances"` // in the configured order
}

// Instance is one Nitter instance's last result, from a fetch or a check.
// A fetch stops at the first instance that answers, so the ones after it
// keep older results.
type Instance struct {
	Host      string    `json:"host"` // scheme://host, as in the config
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	LatencyMs int64     `json:"latencyMs"`
	At        time.Time `json:"at,omitzero"` // zero: not tried yet
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

// CheckResult is POST /checks/{feature}. Feature ids are the panel's: x,
// tg, chat and gh. The feed, chan and dnsm features have no check here.
type CheckResult struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary"` // one sentence for the operator
	Detail  string `json:"detail"`  // each instance or data centre, as logged
}

// Client is what the admin panel needs from the running server. New returns
// the real one; the panel's tests fake it.
type Client interface {
	Status(ctx context.Context) (*Status, error)
	ReloadChannels(ctx context.Context) error
	Refresh(ctx context.Context) error
	SetAnswering(ctx context.Context, on bool) error
	Check(ctx context.Context, feature string) (*CheckResult, error)
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

func (c *client) Check(ctx context.Context, feature string) (*CheckResult, error) {
	var res CheckResult
	if err := c.do(ctx, http.MethodPost, "/checks/"+url.PathEscape(feature), nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
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
