package settings

import (
	"context"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

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
// whether one of those name servers has one of this server's addresses.
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

func (publicDNS) NS(ctx context.Context, domain string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ns, err := resolver.LookupNS(ctx, domain)
	var out []string
	for _, n := range ns {
		out = append(out, strings.TrimSuffix(n.Host, "."))
	}
	return out, err
}

func (publicDNS) Addrs(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return resolver.LookupHost(ctx, host)
}

// Here is this machine's interface addresses.
//
// ponytail: behind NAT (some clouds) the public address isn't on an
// interface, so every domain reads "doesn't point here"; ask an outside
// "what's my IP" service if that matters.
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
