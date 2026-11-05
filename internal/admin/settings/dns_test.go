package settings

import (
	"context"
	"net"
	"slices"
	"testing"

	"github.com/miekg/dns"
)

// Once a domain is delegated, resolvers pass on what its own name server
// says, and thefeed-server answers NXDOMAIN for its domain. The NS record
// the operator set is the parent zone's delegation.
func TestNSIsTheParentZonesDelegation(t *testing.T) {
	// One fake answers as resolver (recursion desired) and as example.com's
	// name server (not).
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, q *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(q)
		m.RecursionAvailable = q.RecursionDesired
		rr := func(s string) dns.RR { r, _ := dns.NewRR(s); return r }
		switch name, typ := q.Question[0].Name, q.Question[0].Qtype; {
		case !q.RecursionDesired && name == "t.example.com." && typ == dns.TypeNS:
			m.Ns = []dns.RR{rr("t.example.com. 300 IN NS ns1.example.com.")}
		case name == "t.example.com.":
			m.Rcode = dns.RcodeNameError // thefeed-server, through a resolver
		case name == "example.com." && typ == dns.TypeNS:
			m.Answer = []dns.RR{rr("example.com. 300 IN NS ns.parent.test.")}
		case name == "ns.parent.test." && typ == dns.TypeA:
			m.Answer = []dns.RR{rr("ns.parent.test. 300 IN A 127.0.0.1")}
		}
		w.WriteMsg(m)
	})}
	go srv.ActivateAndServe()
	t.Cleanup(func() { srv.Shutdown() })
	_, port, _ := net.SplitHostPort(pc.LocalAddr().String())
	resolvers, nsp := publicResolvers, nsPort
	publicResolvers, nsPort = []string{pc.LocalAddr().String()}, port
	t.Cleanup(func() { publicResolvers, nsPort = resolvers, nsp })

	ns, err := publicDNS{}.NS(context.Background(), "t.example.com")
	if err != nil || !slices.Equal(ns, []string{"ns1.example.com"}) {
		t.Errorf("NS = %q, %v; want ns1.example.com, example.com's delegation", ns, err)
	}
}
