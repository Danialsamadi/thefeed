package serverconfig

import "testing"

func TestValidate(t *testing.T) {
	for _, c := range []struct{ key, value, want string }{
		{"chat.sendPerHour", "30", ""},
		{"chat.sendPerHour", " 65535 ", ""},
		{"chat.sendPerHour", "0", "Use a whole number from 1 to 65,535."},
		{"chat.sendPerHour", "", "Use a whole number from 1 to 65,535."},
		{"chat.sendPerHour", "3.5", "Use a whole number from 1 to 65,535."},
		{"media.dnsTTLMin", "525601", "Use a whole number from 1 to 525,600."},
		{"chat.maxAccounts", "-1", "Use a whole number from 0 to 100,000,000."},
		{"chat.syncSec", "0", ""},
		{"fetch.every", "2", "Use a whole number from 3 to 1,440."},
		{"chat.on", "TRUE", ""},
		{"chat.on", "off", ""},
		{"chat.on", "maybe", "Use true or false."},
		{"media.dnsComp", "deflate", ""},
		{"media.dnsComp", "zstd", "Use none, gzip or deflate."},
		{"media.ghRepo", "owner/relay-repo", ""},
		{"media.ghRepo", "", ""},
		{"media.ghRepo", "https://github.com/owner/repo", "Use owner/name, for example owner/relay-repo."},
		{"media.ghBranch", "release/v1", ""},
		{"media.ghBranch", "two words", "Type a branch name, for example main."},
		{"server.listen", ":53", ""},
		{"server.listen", "[::]:5300", ""},
		{"server.listen", "53", "Use host:port, for example :53 or 0.0.0.0:53."},
		{"dns.domain", "t.example.com", ""},
		{"dns.domain", "t.example.com.", ""},
		{"dns.domain", "not a domain", "Type a domain like t.example.com."},
		{"dns.extraDomains", "t2.example.com, t3.example.net", ""},
		{"dns.extraDomains", "t2.example.com,bad_domain", "Type a domain like t.example.com."},
		{"xacc.instances", "https://nitter.net,http://nitter.net", ""},
		{"xacc.instances", "nitter.net", ""},
		{"fetch.apiID", "0", ""},
		{"fetch.apiID", "12ab", "Use the API ID's digits, from my.telegram.org."},
		{"fetch.phone", "none", ""},
		{"no.such", "1", `unknown setting "no.such"`},
	} {
		got := ""
		if err := Validate(c.key, c.value); err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("Validate(%q, %q) = %q, want %q", c.key, c.value, got, c.want)
		}
	}
}

func TestRegistryShape(t *testing.T) {
	for _, s := range Settings {
		if s.Applies != "restart" {
			t.Errorf("%s applies %q; only public channels apply live, and they are a list", s.Key, s.Applies)
		}
		if s.Type == TypeInt && s.Min > s.Max {
			t.Errorf("%s: min %d > max %d", s.Key, s.Min, s.Max)
		}
	}
	for _, key := range []string{"server.key", "fetch.apiHash", "media.ghToken"} {
		if s, _ := Lookup(key); !s.Secret {
			t.Errorf("%s is not marked secret", key)
		}
	}
}
