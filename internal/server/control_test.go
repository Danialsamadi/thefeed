package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/sartoopjj/thefeed/internal/controlapi"
	"github.com/sartoopjj/thefeed/internal/protocol"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// Distinctive secrets, so a leak anywhere in /status shows up as a substring.
const (
	ctlKey     = "ctl-passphrase-7f3a9c"
	ctlAPIHash = "ctl-apihash-0b1d2e3f"
	ctlToken   = "ghp_ctltoken5a6b7c8d"
)

type ctlServer struct {
	dir     string
	dnsAddr string
	api     controlapi.Client
	tmeHits *atomic.Int32 // requests the fake t.me has had
	stop    func()        // shut the server down and wait for it
}

// startCtl runs a whole thefeed-server on a temporary data directory: no
// login, one public channel, one private invite and one X account, with
// fake t.me and Nitter upstreams, and the chat and tunnel domains on.
//
// Keep test names short: macOS caps a Unix socket path at 104 bytes and the
// test name is part of t.TempDir().
func startCtl(t *testing.T, tune func(*Config)) *ctlServer {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"channels.txt":         "@news\n",
		"private_channels.txt": "https://t.me/+AbCdEf123456\n",
		"x_accounts.txt":       "jack\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var tmeHits atomic.Int32
	tme := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tmeHits.Add(1)
		fmt.Fprint(w, `<div class="tgme_channel_info"></div>`)
	}))
	t.Cleanup(tme.Close)
	prev := telegramWebBases
	telegramWebBases = []string{tme.URL + "/s"}
	t.Cleanup(func() { telegramWebBases = prev })
	nitter := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(nitter.Close)

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dnsAddr := pc.LocalAddr().String()
	pc.Close()

	effective := map[string]string{}
	for _, s := range serverconfig.Settings {
		effective[s.Key] = s.Default
	}
	effective["dns.domain"] = "t.example.com"
	effective["server.key"] = ctlKey
	effective["fetch.apiHash"] = ctlAPIHash
	effective["media.ghToken"] = ctlToken

	cfg := Config{
		ListenAddr:          dnsAddr,
		Domain:              "t.example.com",
		Passphrase:          ctlKey,
		DataDir:             dir,
		ChannelsFile:        filepath.Join(dir, "channels.txt"),
		PrivateChannelsFile: filepath.Join(dir, "private_channels.txt"),
		XAccountsFile:       filepath.Join(dir, "x_accounts.txt"),
		XRSSInstances:       nitter.URL,
		NoTelegram:          true,
		DNSMediaEnabled:     true,
		ChatDomains:         []string{"c.example.com"},
		ChatEnabled:         true,
		MTProtoDomains:      []string{"m.example.com"},
		MTProtoEnabled:      true,
		MTProtoMaxTunnels:   7,
		GitHubRelay:         GitHubRelayConfig{Token: ctlToken, Repo: "owner/relay"}, // off: not enabled
		Telegram:            TelegramConfig{APIHash: ctlAPIHash},
		ControlSocket:       true,
		Effective:           effective,
	}
	if tune != nil {
		tune(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	t.Cleanup(stop)

	c := &ctlServer{dir: dir, dnsAddr: dnsAddr, api: controlapi.New(filepath.Join(dir, controlapi.SocketName)), tmeHits: &tmeHits, stop: stop}
	if cfg.ControlSocket {
		waitFor(t, "the control socket", func() bool {
			_, err := c.api.Status(context.Background())
			return err == nil
		})
	}
	return c
}

// query sends one real DNS query for a feed block and returns the decoded
// block, or an error if nothing came back in time.
func (c *ctlServer) query(ch, blk uint16, timeout time.Duration) ([]byte, error) {
	qk, rk, err := protocol.DeriveKeys(ctlKey)
	if err != nil {
		return nil, err
	}
	name, err := protocol.EncodeQuery(qk, ch, blk, "t.example.com", protocol.QuerySingleLabel)
	if err != nil {
		return nil, err
	}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
	resp, _, err := (&dns.Client{Timeout: timeout}).Exchange(m, c.dnsAddr)
	if err != nil {
		return nil, err
	}
	if len(resp.Answer) == 0 {
		return nil, fmt.Errorf("rcode %s, no answer", dns.RcodeToString[resp.Rcode])
	}
	return protocol.DecodeResponse(rk, strings.Join(resp.Answer[0].(*dns.TXT).Txt, ""))
}

// queryUntilAnswered retries, for a listener that may still be binding.
func (c *ctlServer) queryUntilAnswered(t *testing.T, ch, blk uint16) []byte {
	t.Helper()
	var data []byte
	waitFor(t, "a DNS answer", func() bool {
		var err error
		data, err = c.query(ch, blk, 500*time.Millisecond)
		return err == nil
	})
	return data
}

// channels reads the channel list a client sees, from the metadata served
// over DNS.
func (c *ctlServer) channels(t *testing.T) []string {
	t.Helper()
	data := c.queryUntilAnswered(t, protocol.MetadataChannel, 0)
	_, blocks, _, err := protocol.PeekExtendedHeader(data)
	if err != nil {
		t.Fatal(err)
	}
	for blk := uint16(1); blk < uint16(blocks); blk++ {
		b, err := c.query(protocol.MetadataChannel, blk, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, b...)
	}
	meta, err := protocol.ParseMetadata(data)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ch := range meta.Channels {
		names = append(names, ch.Name)
	}
	return names
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCtlStatus(t *testing.T) {
	before := time.Now()
	c := startCtl(t, nil)
	st, err := c.api.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if st.Server.Version != "dev" {
		t.Errorf("version = %q, want dev (the unreleased build)", st.Server.Version)
	}
	if st.Server.Started.Before(before.Add(-time.Second)) || st.Server.Started.After(time.Now()) {
		t.Errorf("started = %v, want between %v and now", st.Server.Started, before)
	}
	if !st.Server.Answering {
		t.Error("a server that just started must be answering")
	}
	if got := st.Server.Config["dns.domain"]; got != "t.example.com" {
		t.Errorf("config dns.domain = %q, want t.example.com", got)
	}
	if got := st.Server.Config["fetch.every"]; got != "10" {
		t.Errorf("config fetch.every = %q, want 10", got)
	}

	want := []controlapi.DomainQueries{{Domain: "t.example.com"}, {Domain: "c.example.com"}, {Domain: "m.example.com"}}
	if fmt.Sprint(st.Feed.Domains) != fmt.Sprint(want) {
		t.Errorf("feed domains = %+v, want %+v", st.Feed.Domains, want)
	}
	if st.Chat == nil || *st.Chat != (controlapi.Chat{}) {
		t.Errorf("chat = %+v, want present and all zero", st.Chat)
	}
	if st.MediaCache == nil {
		t.Error("mediaCache missing with the DNS media relay on")
	}
	if st.GHRelay != nil {
		t.Errorf("ghRelay = %+v, want absent with the relay off", st.GHRelay)
	}
	if st.MTProto == nil || *st.MTProto != (controlapi.MTProto{MaxTunnels: 7}) {
		t.Errorf("mtproto = %+v, want no sessions and a cap of 7", st.MTProto)
	}
}

func TestCtlFeedCounts(t *testing.T) {
	c := startCtl(t, nil)
	c.queryUntilAnswered(t, protocol.MetadataChannel, 0)
	for i := 0; i < 2; i++ {
		if _, err := c.query(protocol.MetadataChannel, 0, 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	want := fmt.Sprint([]controlapi.DomainQueries{
		{Domain: "t.example.com", Queries: 3, PerMinute: 3}, {Domain: "c.example.com"}, {Domain: "m.example.com"},
	})
	waitFor(t, "3 queries counted", func() bool {
		st, err := c.api.Status(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(st.Feed.Domains) == want
	})
}

// The feed's order is public channels, then private ones, then X; a reload
// must keep the private and X slots where a fresh start would put them.
func TestCtlReload(t *testing.T) {
	c := startCtl(t, nil)
	if got, want := fmt.Sprint(c.channels(t)), "[news pv:94eb7a80 x/jack]"; got != want {
		t.Fatalf("channels at start = %s, want %s", got, want)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "channels.txt"), []byte("@news\n@daily\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.api.ReloadChannels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(c.channels(t)), "[news daily pv:94eb7a80 x/jack]"; got != want {
		t.Errorf("channels after reload = %s, want %s", got, want)
	}
}

func TestCtlAnswering(t *testing.T) {
	c := startCtl(t, nil)
	ctx := context.Background()
	c.queryUntilAnswered(t, protocol.MetadataChannel, 0)

	if code, body := c.raw(t, "POST", "/answering", "{}"); code != http.StatusBadRequest {
		t.Errorf("POST /answering {} = %d %s, want 400: a missing on must not pause the server", code, body)
	}
	if err := c.api.SetAnswering(ctx, false); err != nil {
		t.Fatal(err)
	}
	if st, err := c.api.Status(ctx); err != nil || st.Server.Answering {
		t.Errorf("status answering = %v (err %v), want false", st != nil && st.Server.Answering, err)
	}
	if _, err := c.query(protocol.MetadataChannel, 0, 700*time.Millisecond); err == nil {
		t.Error("a feed query was answered while answering is off")
	}
	// Not even a refusal for a zone the server doesn't serve.
	m := new(dns.Msg)
	m.SetQuestion("x.other.example.org.", dns.TypeTXT)
	if _, _, err := (&dns.Client{Timeout: 700 * time.Millisecond}).Exchange(m, c.dnsAddr); err == nil {
		t.Error("a foreign query got a response while answering is off")
	}

	if err := c.api.SetAnswering(ctx, true); err != nil {
		t.Fatal(err)
	}
	c.queryUntilAnswered(t, protocol.MetadataChannel, 0)
	if st, err := c.api.Status(ctx); err != nil || !st.Server.Answering {
		t.Errorf("status answering = %v (err %v), want true", st != nil && st.Server.Answering, err)
	}
}

func TestCtlRefresh(t *testing.T) {
	c := startCtl(t, nil)
	// The first cycle reads @news's page twice: posts, then the avatar.
	waitFor(t, "the first fetch cycle", func() bool { return c.tmeHits.Load() == 2 })
	if err := c.api.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a fetch cycle after refresh", func() bool { return c.tmeHits.Load() > 2 })
}

func TestCtlSocket(t *testing.T) {
	// A crash leaves the socket file behind; the next start must not trip on it.
	c := startCtl(t, func(cfg *Config) {
		l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(cfg.DataDir, controlapi.SocketName), Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		l.SetUnlinkOnClose(false)
		l.Close()
	})
	path := filepath.Join(c.dir, controlapi.SocketName)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Errorf("control socket mode = %v, want a socket with 0600", fi.Mode())
	}
	c.stop()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("control socket still there after shutdown: %v", err)
	}
}

func TestCtlSocketOff(t *testing.T) {
	c := startCtl(t, func(cfg *Config) { cfg.ControlSocket = false })
	c.queryUntilAnswered(t, protocol.MetadataChannel, 0)
	if _, err := os.Stat(filepath.Join(c.dir, controlapi.SocketName)); !os.IsNotExist(err) {
		t.Errorf("control socket exists with the setting off: %v", err)
	}
}

// raw sends a request to the control socket without the client, to see the
// bytes on the wire.
func (c *ctlServer) raw(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	hc := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return net.Dial("unix", filepath.Join(c.dir, controlapi.SocketName))
	}}}
	req, err := http.NewRequest(method, "http://x"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestCtlNoSecrets(t *testing.T) {
	c := startCtl(t, nil)
	_, body := c.raw(t, "GET", "/status", "")
	for _, secret := range []string{ctlKey, ctlAPIHash, ctlToken} {
		if strings.Contains(string(body), secret) {
			t.Errorf("/status contains the secret %q:\n%s", secret, body)
		}
	}

	st, err := c.api.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range serverconfig.Settings {
		_, ok := st.Server.Config[s.Key]
		if s.Secret && ok {
			t.Errorf("config has the secret setting %s", s.Key)
		}
		if !s.Secret && !ok {
			t.Errorf("config is missing %s", s.Key)
		}
	}
}
