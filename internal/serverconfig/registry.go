// Package serverconfig describes thefeed-server's settings, and reads and
// writes the files that hold them, for both the server and the admin panel,
// so the two judge every value the same way.
package serverconfig

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Type is how a setting's value is written in thefeed.env.
type Type string

const (
	TypeInt    Type = "int"
	TypeBool   Type = "bool"
	TypeString Type = "string"
	TypeEnum   Type = "enum"
	TypeList   Type = "list" // comma-separated
)

// Setting is one server setting. Values are strings as written in
// thefeed.env; numbers are in the env variable's own unit (KB, minutes...).
type Setting struct {
	Key      string // stable id the panel uses
	Env      string
	Flag     string // without the leading dashes
	Type     Type
	Min, Max int      // Int only
	Allowed  []string // Enum only
	Default  string
	Page     string // Control page id; "" when the panel doesn't show it
	Applies  string // "live" or "restart"
	Secret   bool   // never shown or returned
	check    func(string) error
}

func num(key, env, flag, page string, min, max, def int) Setting {
	return Setting{Key: key, Env: env, Flag: flag, Type: TypeInt, Min: min, Max: max, Default: strconv.Itoa(def), Page: page, Applies: "restart"}
}

func onOff(key, env, flag, page string, def bool) Setting {
	return Setting{Key: key, Env: env, Flag: flag, Type: TypeBool, Default: strconv.FormatBool(def), Page: page, Applies: "restart"}
}

func text(key, env, flag, page, def string, check func(string) error) Setting {
	return Setting{Key: key, Env: env, Flag: flag, Type: TypeString, Default: def, Page: page, Applies: "restart", check: check}
}

func list(key, env, flag, page string, item func(string) error) Setting {
	return Setting{Key: key, Env: env, Flag: flag, Type: TypeList, Page: page, Applies: "restart", check: item}
}

func secret(s Setting) Setting { s.Secret = true; return s }

// Settings holds every server flag that has an env equivalent, which is all
// of them except --env-file and the one-shot actions (--login-only,
// --print-*, --report*, --version).
//
// Every setting applies at restart. Public channels are the one thing that
// applies live, and they are a list file, not a setting.
//
// ponytail: maximums are generous on purpose. A server that booted before
// must still boot, and it used to take any number at or above the minimum.
var Settings = []Setting{
	text("server.dataDir", "THEFEED_DATA_DIR", "data-dir", "", "./data", nil),
	text("server.listen", "THEFEED_LISTEN", "listen", "", ":53", checkListen),
	// The admin panel talks to the server through this socket, so the panel
	// doesn't offer to turn it off.
	onOff("server.controlSocket", "THEFEED_CONTROL_SOCKET", "control-socket", "", true),
	text("dns.domain", "THEFEED_DOMAIN", "domain", "dns", "", checkDomain),
	list("dns.extraDomains", "THEFEED_EXTRA_DOMAINS", "extra-domains", "dns", checkDomain),
	secret(text("server.key", "THEFEED_KEY", "key", "", "", nil)),
	text("chan.file", "THEFEED_CHANNELS", "channels", "", "", nil),
	text("chan.privateFile", "THEFEED_PRIVATE_CHANNELS", "private-channels", "", "", nil),
	text("xacc.file", "THEFEED_X_ACCOUNTS", "x-accounts", "", "", nil),
	list("xacc.instances", "THEFEED_X_RSS_INSTANCES", "x-rss-instances", "xacc", func(s string) error { _, err := ParseInstance(s); return err }),
	text("fetch.apiID", "TELEGRAM_API_ID", "api-id", "", "", checkDigits),
	secret(text("fetch.apiHash", "TELEGRAM_API_HASH", "api-hash", "", "", nil)),
	text("fetch.phone", "TELEGRAM_PHONE", "phone", "", "", nil),
	onOff("fetch.noTelegram", "THEFEED_NO_TELEGRAM", "no-telegram", "fetch", false),
	text("fetch.session", "THEFEED_SESSION", "session", "", "", nil),
	// EncodeResponse draws the padding from one random byte, so 255 is the most it adds.
	num("dns.padding", "THEFEED_PADDING", "padding", "dns", 0, 255, 32),
	// Telegram returns at most 100 messages a request.
	num("fetch.msgs", "THEFEED_MSG_LIMIT", "msg-limit", "fetch", 1, 100, 15),
	num("fetch.every", "THEFEED_FETCH_INTERVAL", "fetch-interval", "fetch", 3, 1440, 10),
	onOff("dns.manage", "THEFEED_ALLOW_MANAGE", "allow-manage", "dns", false),
	onOff("dns.debug", "THEFEED_DEBUG", "debug", "dns", false),
	onOff("media.dnsOn", "THEFEED_DNS_MEDIA_ENABLED", "dns-media-enabled", "media", false),
	num("media.dnsMaxKB", "THEFEED_DNS_MEDIA_MAX_SIZE_KB", "dns-media-max-size", "media", 0, 1<<20, 100),
	num("media.dnsTTLMin", "THEFEED_DNS_MEDIA_CACHE_TTL_MIN", "dns-media-cache-ttl", "media", 1, 525600, 600),
	{Key: "media.dnsComp", Env: "THEFEED_DNS_MEDIA_COMPRESSION", Flag: "dns-media-compression", Type: TypeEnum,
		Allowed: []string{"none", "gzip", "deflate"}, Default: "gzip", Page: "media", Applies: "restart"},
	list("chat.domains", "THEFEED_CHAT_DOMAINS", "chat-domains", "dns", checkDomain),
	onOff("chat.on", "THEFEED_CHAT_ENABLED", "chat-enabled", "chat", true),
	// The messenger limits ride uint16 on the wire.
	num("chat.sendPerHour", "THEFEED_CHAT_SEND_PER_HOUR", "chat-send-per-hour", "chat", 1, 65535, 30),
	num("chat.inboxCap", "THEFEED_CHAT_INBOX_CAP", "chat-inbox-cap", "chat", 1, 65535, 50),
	num("chat.perPairCap", "THEFEED_CHAT_PER_PAIR_CAP", "chat-per-pair-cap", "chat", 1, 65535, 10),
	num("chat.maxMsgBytes", "THEFEED_CHAT_MAX_MSG_BYTES", "chat-max-msg-bytes", "chat", 1, 65535, 500),
	num("chat.ttlHours", "THEFEED_CHAT_TTL_HOURS", "chat-ttl-hours", "chat", 1, 65535, 72),
	num("chat.accountTTLDays", "THEFEED_CHAT_ACCOUNT_TTL_DAYS", "chat-account-ttl-days", "chat", 0, 36500, 0),
	num("chat.maxAccounts", "THEFEED_CHAT_MAX_ACCOUNTS", "chat-max-accounts", "chat", 0, 100_000_000, 0),
	num("chat.syncSec", "THEFEED_CHAT_SYNC_SECONDS", "chat-sync-seconds", "chat", 0, 3600, 1),
	list("mt.domains", "THEFEED_MTPROTO_DOMAINS", "mtproto-domains", "dns", checkDomain),
	onOff("mt.on", "THEFEED_MTPROTO_ENABLED", "mtproto-enabled", "mt", true),
	num("mt.maxTunnels", "THEFEED_MTPROTO_MAX_TUNNELS", "mtproto-max-tunnels", "mt", 0, 1_000_000, 0),
	num("mt.idleMin", "THEFEED_MTPROTO_IDLE_MINUTES", "mtproto-idle-minutes", "mt", 1, 1440, 5),
	num("mt.bufferKB", "THEFEED_MTPROTO_BUFFER_KB", "mtproto-buffer-kb", "mt", 1, 65536, 32),
	num("mt.downKBMin", "THEFEED_MTPROTO_DOWN_KB_PER_MIN", "mtproto-down-kb-per-min", "mt", 0, 10_000_000, 512),
	num("mt.dialSec", "THEFEED_MTPROTO_DIAL_SECONDS", "mtproto-dial-seconds", "mt", 1, 600, 15),
	onOff("mt.testDCs", "THEFEED_MTPROTO_TEST_DCS", "mtproto-test-dcs", "", false),
	onOff("media.ghOn", "THEFEED_GITHUB_RELAY_ENABLED", "github-relay-enabled", "media", false),
	secret(text("media.ghToken", "THEFEED_GITHUB_RELAY_TOKEN", "github-relay-token", "media", "", nil)),
	text("media.ghRepo", "THEFEED_GITHUB_RELAY_REPO", "github-relay-repo", "media", "", checkRepo),
	text("media.ghBranch", "THEFEED_GITHUB_RELAY_BRANCH", "github-relay-branch", "media", "main", checkBranch),
	// GitHub refuses files over 100 MB.
	num("media.ghMaxKB", "THEFEED_GITHUB_RELAY_MAX_SIZE_KB", "github-relay-max-size", "media", 0, 102400, 15*1024),
	num("media.ghTTLMin", "THEFEED_GITHUB_RELAY_TTL_MIN", "github-relay-ttl", "media", 1, 525600, 600),
	num("media.ghQuotaGB", "THEFEED_GITHUB_RELAY_QUOTA_GB", "github-relay-quota-gb", "media", 1, 10000, 100),
}

// Lookup finds a setting by its key.
func Lookup(key string) (Setting, bool) {
	for _, s := range Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}

// Validate checks a value as it would be written in thefeed.env. The error's
// text is meant for the operator, next to the field. An empty value is only
// valid for strings and lists, where it means "not set".
func Validate(key, value string) error {
	s, ok := Lookup(key)
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	v := strings.TrimSpace(value)
	switch s.Type {
	case TypeInt:
		n, err := strconv.Atoi(v)
		if err != nil || n < s.Min || n > s.Max {
			return fmt.Errorf("Use a whole number from %s to %s.", thousands(s.Min), thousands(s.Max))
		}
	case TypeBool:
		if _, ok := ParseBool(v); !ok {
			return errors.New("Use true or false.")
		}
	case TypeEnum:
		for _, a := range s.Allowed {
			if v == a {
				return nil
			}
		}
		last := len(s.Allowed) - 1
		return fmt.Errorf("Use %s or %s.", strings.Join(s.Allowed[:last], ", "), s.Allowed[last])
	case TypeString:
		if s.check != nil && v != "" {
			return s.check(v)
		}
	case TypeList:
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				if err := s.check(item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ParseBool reads a boolean the way the server always has: 1/true/yes/on or
// 0/false/no/off, in any case. ok is false for anything else.
func ParseBool(s string) (v, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// thousands writes 10080 as "10,080".
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

var (
	domainRE = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+\.?$`)
	repoRE   = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
	branchRE = regexp.MustCompile(`^[\w./-]+$`)
	digitsRE = regexp.MustCompile(`^\d+$`)
)

func checkDomain(s string) error {
	if !domainRE.MatchString(s) {
		return errors.New("Type a domain like t.example.com.")
	}
	return nil
}

func checkListen(s string) error {
	_, port, err := net.SplitHostPort(s)
	if p, perr := strconv.Atoi(port); err != nil || perr != nil || p < 0 || p > 65535 {
		return errors.New("Use host:port, for example :53 or 0.0.0.0:53.")
	}
	return nil
}

func checkRepo(s string) error {
	if !repoRE.MatchString(s) {
		return errors.New("Use owner/name, for example owner/relay-repo.")
	}
	return nil
}

func checkBranch(s string) error {
	if !branchRE.MatchString(s) {
		return errors.New("Type a branch name, for example main.")
	}
	return nil
}

func checkDigits(s string) error {
	if !digitsRE.MatchString(s) {
		return errors.New("Use the API ID's digits, from my.telegram.org.")
	}
	return nil
}
