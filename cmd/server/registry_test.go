package main

import (
	"flag"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// Flags that are not settings: where to read settings from, and the
// one-shot actions, which never get an env variable.
var notSettings = map[string]bool{
	"env-file": true, "login-only": true, "version": true, "print-config": true, "print-pubkey": true,
	"report": true, "report-refresh": true, "report-top": true, "report-from": true, "report-to": true,
}

// The admin panel validates against the registry and the server parses its
// flags, so the two must agree on every name and default.
func TestRegistryMatchesServerFlags(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	flags := map[string]string{} // name -> default as written in thefeed.env
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 3 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "flag" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		name, _ := strconv.Unquote(lit.Value)
		tv, err := types.Eval(token.NewFileSet(), nil, token.NoPos, types.ExprString(call.Args[1]))
		if err != nil {
			t.Fatalf("--%s default: %v", name, err)
		}
		switch tv.Value.Kind() {
		case constant.String:
			flags[name] = constant.StringVal(tv.Value)
		default:
			flags[name] = tv.Value.ExactString()
		}
		return true
	})
	if len(flags) < 40 {
		t.Fatalf("found only %d flags in main.go; the parser is missing them", len(flags))
	}

	keys, envs := map[string]bool{}, map[string]bool{}
	for _, s := range serverconfig.Settings {
		if keys[s.Key] || envs[s.Env] {
			t.Errorf("%s / %s is registered twice", s.Key, s.Env)
		}
		keys[s.Key], envs[s.Env] = true, true
		if !strings.Contains(string(src), `"`+s.Env+`"`) {
			t.Errorf("%s: the server never reads %s", s.Key, s.Env)
		}
		def, ok := flags[s.Flag]
		if !ok {
			t.Errorf("%s: the server has no --%s", s.Key, s.Flag)
			continue
		}
		delete(flags, s.Flag)
		if def != s.Default {
			t.Errorf("--%s defaults to %q, the registry says %q", s.Flag, def, s.Default)
		}
		if err := serverconfig.Validate(s.Key, s.Default); s.Default != "" && err != nil {
			t.Errorf("%s: the default %q is refused: %v", s.Key, s.Default, err)
		}
	}
	for name := range flags {
		if !notSettings[name] {
			t.Errorf("--%s is not in the settings registry", name)
		}
	}
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// A server the installer set up must still start: these are the values its
// env file gets in both modes.
func TestCheckSettingsAcceptsInstallerEnv(t *testing.T) {
	base := map[string]string{
		"THEFEED_DOMAIN": "t.example.com", "THEFEED_EXTRA_DOMAINS": "", "THEFEED_CHAT_DOMAINS": "",
		"THEFEED_MTPROTO_DOMAINS": "", "THEFEED_MTPROTO_MAX_TUNNELS": "0", "THEFEED_KEY": "some passphrase $x",
		"THEFEED_ALLOW_MANAGE": "0", "THEFEED_MSG_LIMIT": "15",
		"THEFEED_X_RSS_INSTANCES": "https://nitter.net,http://nitter.net",
		"THEFEED_LISTEN":          "0.0.0.0:53", "THEFEED_FETCH_INTERVAL": "10",
		"THEFEED_DNS_MEDIA_ENABLED": "1", "THEFEED_DNS_MEDIA_MAX_SIZE_KB": "100",
		"THEFEED_DNS_MEDIA_CACHE_TTL_MIN": "600", "THEFEED_DNS_MEDIA_COMPRESSION": "gzip",
		"THEFEED_GITHUB_RELAY_ENABLED": "0", "THEFEED_GITHUB_RELAY_TOKEN": "", "THEFEED_GITHUB_RELAY_REPO": "",
		"THEFEED_GITHUB_RELAY_BRANCH": "main", "THEFEED_GITHUB_RELAY_MAX_SIZE_KB": "15360",
		"THEFEED_GITHUB_RELAY_TTL_MIN": "600", "THEFEED_CHAT_SEND_PER_HOUR": "30",
		"THEFEED_CHAT_ACCOUNT_TTL_DAYS": "0", "THEFEED_CHAT_MAX_ACCOUNTS": "0",
	}
	noTelegram := map[string]string{"TELEGRAM_API_ID": "0", "TELEGRAM_API_HASH": "none", "TELEGRAM_PHONE": "none", "THEFEED_NO_TELEGRAM": "1"}
	telegram := map[string]string{"TELEGRAM_API_ID": "123456", "TELEGRAM_API_HASH": "0123456789abcdef", "TELEGRAM_PHONE": "+15551234567"}
	for name, mode := range map[string]map[string]string{"no-telegram": noTelegram, "telegram": telegram} {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range mode {
			env[k] = v
		}
		if err := checkSettings(map[string]bool{}, envOf(env)); err != nil {
			t.Errorf("%s install refused:\n%v", name, err)
		}
	}
}

// A value the panel would refuse stops the server, where it used to be
// dropped for the default without a word.
func TestCheckSettingsRefusesWhatThePanelRefuses(t *testing.T) {
	for _, c := range [][3]string{
		{"THEFEED_CHAT_SEND_PER_HOUR", "-1", "Use a whole number from 1 to 65,535."},
		{"THEFEED_FETCH_INTERVAL", "2", "Use a whole number from 3 to 1,440."},
		{"THEFEED_MSG_LIMIT", "lots", "Use a whole number from 1 to 100."},
		{"THEFEED_NO_TELEGRAM", "maybe", "Use true or false."},
		{"THEFEED_DNS_MEDIA_COMPRESSION", "zstd", "Use none, gzip or deflate."},
		{"THEFEED_X_RSS_INSTANCES", "ftp://nitter.net", "Type a host like nitter.example.com, or a link like https://nitter.example.com."},
		{"THEFEED_GITHUB_RELAY_REPO", "relay-repo", "Use owner/name, for example owner/relay-repo."},
	} {
		want := c[0] + "=" + c[1] + ": " + c[2]
		err := checkSettings(map[string]bool{}, envOf(map[string]string{c[0]: c[1]}))
		if err == nil || err.Error() != want {
			t.Errorf("got %v, want %q", err, want)
		}
	}
	// Older servers read 0 in env as the default, so a server set up that way must still start.
	if err := checkSettings(map[string]bool{}, envOf(map[string]string{
		"THEFEED_CHAT_SEND_PER_HOUR": "0", "THEFEED_CHAT_SYNC_SECONDS": "0", "THEFEED_MTPROTO_DOWN_KB_PER_MIN": "0",
	})); err != nil {
		t.Error(err)
	}
}

// A typed flag is checked, and so is its env value: several overlays apply
// the env value anyway, when the flag equals its default or always.
func TestCheckSettingsChecksTheTypedFlag(t *testing.T) {
	if flag.Lookup("fetch-interval") == nil {
		flag.Int("fetch-interval", 10, "")
	}
	given := map[string]bool{"fetch-interval": true}
	if err := flag.Set("fetch-interval", "2"); err != nil {
		t.Fatal(err)
	}
	err := checkSettings(given, envOf(map[string]string{"THEFEED_FETCH_INTERVAL": "10"}))
	if want := "--fetch-interval=2: Use a whole number from 3 to 1,440."; err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
	if err := flag.Set("fetch-interval", "10"); err != nil {
		t.Fatal(err)
	}
	// --fetch-interval=10 is the default, so the server applies the env's 2.
	err = checkSettings(given, envOf(map[string]string{"THEFEED_FETCH_INTERVAL": "2"}))
	if want := "THEFEED_FETCH_INTERVAL=2: Use a whole number from 3 to 1,440."; err == nil || err.Error() != want {
		t.Errorf("got %v, want %q", err, want)
	}
}
