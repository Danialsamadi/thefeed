package serverconfig

import (
	"fmt"
	"regexp"
	"strings"
)

// ParseInviteHash extracts the invite hash from any supported
// invite-link shape: t.me/+HASH, t.me/joinchat/HASH, tg://join?invite=HASH,
// or bare HASH (with or without leading +). Public-username URLs
// (t.me/foo without + or joinchat/) are rejected.
func ParseInviteHash(link string) (string, error) {
	s := strings.TrimSpace(link)
	if s == "" {
		return "", fmt.Errorf("empty invite link")
	}

	// tg://join?invite=HASH or telegram://join?invite=HASH — capture
	// the `invite=` param before any query-string stripping.
	if lower := strings.ToLower(s); strings.HasPrefix(lower, "tg://") || strings.HasPrefix(lower, "telegram://") {
		if i := strings.Index(s, "invite="); i >= 0 {
			h := s[i+len("invite="):]
			if j := strings.IndexAny(h, "&#"); j >= 0 {
				h = h[:j]
			}
			return validateHash(h)
		}
		return "", fmt.Errorf("not an invite link: %q (tg://join requires ?invite=HASH)", link)
	}

	// Strip query / fragment and trailing slashes.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "/")

	// Strip URL scheme.
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "www.")

	// If a t.me-family host is present, require /+HASH or /joinchat/HASH
	// after it. Anything else (e.g. t.me/publicchannel) is a public
	// username and doesn't belong here.
	for _, host := range []string{"t.me/", "telegram.me/", "telegram.dog/"} {
		if strings.HasPrefix(s, host) {
			rest := s[len(host):]
			if strings.HasPrefix(rest, "+") {
				return validateHash(rest[1:])
			}
			if strings.HasPrefix(rest, "joinchat/") {
				return validateHash(rest[len("joinchat/"):])
			}
			return "", fmt.Errorf("not an invite link: %q (expected /+HASH or /joinchat/HASH after %s)", link, host)
		}
	}

	// Bare form: +HASH or HASH (no URL host).
	s = strings.TrimPrefix(s, "+")
	return validateHash(s)
}

var inviteHashRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

func validateHash(h string) (string, error) {
	h = strings.TrimSpace(h)
	if !inviteHashRE.MatchString(h) {
		return "", fmt.Errorf("invalid invite hash %q (expected 8–64 chars of [A-Za-z0-9_-])", h)
	}
	return h, nil
}

// LooksLikeInvite tells an invite link from a channel name by its shape
// alone: ParseInviteHash also takes a bare hash, so "daily_brief" would
// parse as one. Use it to file what an operator pastes under the right list.
func LooksLikeInvite(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "+") || strings.Contains(s, "/+") || strings.Contains(s, "joinchat/") ||
		strings.HasPrefix(s, "tg://") || strings.HasPrefix(s, "telegram://")
}
