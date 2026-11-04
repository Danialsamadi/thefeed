package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/protocol"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// Module is the health module on the real server: its control socket, and
// its DNS listener with the key from thefeed.env.
func Module(d *admin.Deps) error {
	return New(d.Control(), feedQuery{admin.EnvFile(d.ServerDataDir)})(d)
}

// feedQuery is the real SelfTest: the feed's first metadata block, asked
// for the way a reader's app does, and decoded with the server's key. The
// key is read from envFile each time, so a new one needs no panel restart.
type feedQuery struct{ envFile string }

func (q feedQuery) Query(ctx context.Context, listen, domain string) (time.Duration, error) {
	raw, err := os.ReadFile(q.envFile)
	if err != nil {
		return 0, err
	}
	key, _ := serverconfig.ParseEnv(raw).Get("THEFEED_KEY")
	if key == "" {
		return 0, fmt.Errorf("THEFEED_KEY isn't set in %s", q.envFile)
	}
	queryKey, responseKey, err := protocol.DeriveKeys(key)
	if err != nil {
		return 0, err
	}
	name, err := protocol.EncodeQuery(queryKey, protocol.MetadataChannel, 0, domain, protocol.QuerySingleLabel)
	if err != nil {
		return 0, err
	}
	// The listener answers on every address when it has none of its own.
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeTXT)
	m.SetEdns0(4096, false)
	resp, rtt, err := (&dns.Client{Timeout: 2 * time.Second}).ExchangeContext(ctx, m, net.JoinHostPort(host, port))
	if err != nil {
		return 0, err
	}
	if resp.Rcode != dns.RcodeSuccess {
		return rtt, fmt.Errorf("answered %s", dns.RcodeToString[resp.Rcode])
	}
	for _, rr := range resp.Answer {
		if txt, ok := rr.(*dns.TXT); ok {
			if _, err := protocol.DecodeResponse(responseKey, strings.Join(txt.Txt, "")); err != nil {
				return rtt, fmt.Errorf("the answer doesn't open with the key in thefeed.env: %w", err)
			}
			return rtt, nil
		}
	}
	return rtt, errors.New("the answer has no TXT record")
}
