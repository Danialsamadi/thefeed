package serverconfig

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Codec reads and writes one of the server's source lists: channels.txt,
// x_accounts.txt, private_channels.txt or the comma-separated Nitter
// instances setting.
type Codec struct {
	header []string // written when the file has none
	prefix string   // "@" before each channels.txt entry
	csv    bool     // one line, comma-separated
	entry  func(string) (string, error)
	id     func(string) string // identity for duplicate checks
	dup    func(string) string // "already on the list" message
}

func lowerID(s string) string { return strings.ToLower(s) }
func atDup(s string) string   { return fmt.Sprintf("@%s is already on the list.", s) }

var (
	Channels = &Codec{
		header: []string{"# Telegram channel usernames (one per line)", "# Lines starting with # are comments"},
		prefix: "@", entry: channelEntry, id: lowerID, dup: atDup,
	}
	XAccounts = &Codec{
		header: []string{"# X usernames (one per line, without @)", "# Lines starting with # are comments"},
		entry:  xEntry, id: lowerID, dup: atDup,
	}
	PrivateChannels = &Codec{
		header: []string{
			"# Telegram private-channel invite links (one per line)",
			"# Server must be logged into Telegram for these to work.",
			"# Lines starting with # are comments",
		},
		entry: inviteEntry,
		// Invite hashes are case-sensitive, so two that differ only in case are two channels.
		id: func(s string) string {
			if h, err := ParseInviteHash(s); err == nil {
				return h
			}
			return s
		},
		dup: func(string) string { return "That invite link is already on the list." },
	}
	Instances = &Codec{
		csv: true, entry: ParseInstance,
		id: func(s string) string {
			if u, err := ParseInstance(s); err == nil {
				return u
			}
			return strings.ToLower(s)
		},
		dup: func(s string) string { return s + " is already on the list." },
	}
)

// List is a parsed list. Items are kept as the file has them, so an old
// entry that today's rules would refuse is still listed and written back;
// only Add and Rename validate.
type List struct {
	Header []string // the leading comment block, kept as it is
	Items  []string // without the "@"
	c      *Codec
}

// Parse reads a list file the way the server does: blank and # lines are
// skipped and a leading "@" is dropped. Comments after the first entry are
// not kept.
func (c *Codec) Parse(data []byte) *List {
	l := &List{c: c}
	if c.csv {
		for _, s := range strings.Split(string(data), ",") {
			if s = strings.TrimSpace(s); s != "" {
				l.Items = append(l.Items, s)
			}
		}
		return l
	}
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			if len(l.Items) == 0 && t != "" {
				l.Header = append(l.Header, strings.TrimRight(line, "\r"))
			}
			continue
		}
		if c != PrivateChannels {
			t = strings.TrimPrefix(t, "@")
		}
		l.Items = append(l.Items, t)
	}
	return l
}

// Bytes writes the list back in its file's format.
func (l *List) Bytes() []byte {
	if l.c.csv {
		return []byte(strings.Join(l.Items, ","))
	}
	header := l.Header
	if len(header) == 0 {
		header = l.c.header
	}
	var b strings.Builder
	for _, h := range header {
		b.WriteString(h + "\n")
	}
	for _, it := range l.Items {
		b.WriteString(l.c.prefix + it + "\n")
	}
	return []byte(b.String())
}

// Add validates what the operator typed, appends it and returns the stored form.
func (l *List) Add(raw string) (string, error) {
	return l.put(-1, raw)
}

// Rename replaces item i with what the operator typed.
func (l *List) Rename(i int, raw string) (string, error) {
	if i < 0 || i >= len(l.Items) {
		return "", errors.New("That entry is no longer on the list.")
	}
	return l.put(i, raw)
}

func (l *List) put(at int, raw string) (string, error) {
	item, err := l.c.entry(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	id := l.c.id(item)
	for i, it := range l.Items {
		if i != at && l.c.id(it) == id {
			return "", errors.New(l.c.dup(item))
		}
	}
	if at < 0 {
		l.Items = append(l.Items, item)
	} else {
		l.Items[at] = item
	}
	return item, nil
}

var (
	channelRE  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{4,31}$`)
	xHandleRE  = regexp.MustCompile(`^\w{1,15}$`)
	tmeHostRE  = regexp.MustCompile(`(?i)^(https?://)?(www\.)?(t\.me|telegram\.me|telegram\.dog)/(s/)?`)
	xHostRE    = regexp.MustCompile(`(?i)^(https?://)?(www\.|mobile\.)?(x\.com|twitter\.com)/`)
	hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
)

const typeFirst = "Type a name or a link first."

// name strips a profile link down to the name: host, "@", then any path,
// query or fragment after it.
func name(s string, host *regexp.Regexp) string {
	s = strings.TrimPrefix(host.ReplaceAllString(s, ""), "@")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

func channelEntry(s string) (string, error) {
	if s == "" {
		return "", errors.New(typeFirst)
	}
	if LooksLikeInvite(s) {
		return "", errors.New("That's an invite link. Add it under Private channels.")
	}
	if n := name(s, tmeHostRE); channelRE.MatchString(n) {
		return n, nil
	}
	return "", errors.New("Channel names are 5 to 32 letters, digits or underscores, starting with a letter.")
}

func xEntry(s string) (string, error) {
	if s == "" {
		return "", errors.New(typeFirst)
	}
	if n := name(s, xHostRE); xHandleRE.MatchString(n) {
		return n, nil
	}
	return "", errors.New("X handles are 1 to 15 letters, digits or underscores.")
}

func inviteEntry(s string) (string, error) {
	if s == "" {
		return "", errors.New("Paste an invite link first.")
	}
	h, err := ParseInviteHash(s)
	if err != nil {
		return "", errors.New("That isn't an invite link. Use t.me/+…, t.me/joinchat/… or tg://join?invite=….")
	}
	return "https://t.me/+" + h, nil
}

// ParseInstance reads one Nitter instance as a host or a link and returns
// it as the server uses it, "scheme://host[:port]" in lower case. A bare
// host gets https.
func ParseInstance(s string) (string, error) {
	bad := errors.New("Type a host like nitter.example.com, or a link like https://nitter.example.com.")
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", bad
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) == nil && !hostnameRE.MatchString(host) {
		return "", bad
	}
	// url.Parse has already refused a port that isn't a number.
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}
