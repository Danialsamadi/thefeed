package settings

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/miekg/dns"
	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

type domainView struct {
	Domain     string   `json:"domain"`
	Serves     string   `json:"serves"` // feed, chat or mtproto
	Main       bool     `json:"main,omitempty"`
	NS         []string `json:"ns,omitempty"` // the check only, from here on
	PointsHere bool     `json:"pointsHere"`
	Error      string   `json:"error,omitempty"`
}

// domains lists what the server answers for, as it runs (or as saved, when
// it doesn't answer).
func (m *module) domains(r *http.Request) ([]domainView, error) {
	f, err := m.load(r.Context())
	if err != nil {
		return nil, err
	}
	out := []domainView{}
	for _, d := range []struct{ key, serves string }{
		{"dns.domain", "feed"}, {"dns.extraDomains", "feed"}, {"chat.domains", "chat"}, {"mt.domains", "mtproto"},
	} {
		s, _ := serverconfig.Lookup(d.key)
		v, ok := f.running[d.key]
		if !ok {
			v = f.saved(s)
		}
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, domainView{Domain: name, Serves: d.serves, Main: d.key == "dns.domain"})
			}
		}
	}
	return out, nil
}

func (m *module) handleDomains(w http.ResponseWriter, r *http.Request) {
	ds, err := m.domains(r)
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't read the settings: " + err.Error()})
		return
	}
	admin.WriteJSON(w, http.StatusOK, map[string]any{"domains": ds})
}

// handleCheck asks public resolvers for each domain's NS records, and
// whether one of those name servers has one of this server's addresses:
// its interfaces', and the one the operator reached the panel at, which is
// the public one behind 1:1 NAT, where no interface has it.
func (m *module) handleCheck(w http.ResponseWriter, r *http.Request) {
	ds, err := m.domains(r)
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't read the settings: " + err.Error()})
		return
	}
	here, err := m.dns.Here(r.Context())
	if err != nil {
		admin.WriteError(w, http.StatusInternalServerError, admin.FieldError{Message: "Couldn't read this server's addresses: " + err.Error()})
		return
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if addrs, err := m.dns.Addrs(r.Context(), host); err == nil {
		here = append(here, addrs...)
	}
	for i := range ds {
		d := &ds[i]
		if d.NS, err = m.dns.NS(r.Context(), d.Domain); err != nil {
			d.Error = err.Error()
			continue
		}
		for _, ns := range d.NS {
			addrs, _ := m.dns.Addrs(r.Context(), ns)
			d.PointsHere = d.PointsHere || slices.ContainsFunc(addrs, func(a string) bool { return slices.Contains(here, a) })
		}
	}
	admin.WriteJSON(w, http.StatusOK, map[string]any{"checkedAt": m.d.Clock.Now(), "domains": ds})
}

// publicResolvers answer the NS check, so it sees what the rest of the
// internet sees rather than this machine's own resolver or cache.
var publicResolvers = []string{"1.1.1.1:53", "8.8.8.8:53", "9.9.9.9:53"}

// nsPort is the port name servers answer on. Tests move it.
var nsPort = "53"

// publicDNS is the real DNS.
type publicDNS struct{}

var resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
	var d net.Dialer
	var err error
	for _, addr := range publicResolvers {
		var c net.Conn
		if c, err = d.DialContext(ctx, network, addr); err == nil {
			return c, nil
		}
	}
	return nil, err
}}

// NS is the domain's NS records as its parent zone delegates them: the
// records the operator set at their DNS host. Resolvers can't tell, since
// they pass on what the domain's own name server answers, and
// thefeed-server answers NXDOMAIN for its domain.
func (publicDNS) NS(ctx context.Context, domain string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	q := dns.Fqdn(domain)
	// The parent is the closest zone above the domain that has name servers.
	var parent []*net.NS
	for name := q; len(parent) == 0; {
		_, up, _ := strings.Cut(name, ".")
		if up == "" {
			return nil, fmt.Errorf("no zone above %s has name servers", domain)
		}
		name = up
		parent, _ = resolver.LookupNS(ctx, name)
	}
	m := new(dns.Msg)
	m.SetQuestion(q, dns.TypeNS)
	m.RecursionDesired = false
	c := &dns.Client{Timeout: 3 * time.Second}
	err := fmt.Errorf("no name server of the zone above %s answered", domain)
	for _, p := range parent {
		addrs, _ := resolver.LookupHost(ctx, p.Host)
		for _, a := range addrs {
			r, _, xerr := c.ExchangeContext(ctx, m, net.JoinHostPort(a, nsPort))
			if xerr != nil {
				err = xerr
				continue
			}
			// A delegation is a referral; a zone the parent serves itself
			// answers.
			var out []string
			for _, rr := range append(r.Answer, r.Ns...) {
				if ns, ok := rr.(*dns.NS); ok && strings.EqualFold(ns.Hdr.Name, q) {
					out = append(out, strings.TrimSuffix(ns.Ns, "."))
				}
			}
			return out, nil
		}
	}
	return nil, err
}

func (publicDNS) Addrs(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return resolver.LookupHost(ctx, host)
}

// Here is this machine's interface addresses.
func (publicDNS) Here(context.Context) ([]string, error) {
	addrs, err := net.InterfaceAddrs()
	var out []string
	for _, a := range addrs {
		if ip, ok := a.(*net.IPNet); ok {
			out = append(out, ip.IP.String())
		}
	}
	return out, err
}
