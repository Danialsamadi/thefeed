package serverconfig

import "testing"

func TestParseInviteHash(t *testing.T) {
	const want = "aBcDeF123-_"

	good := []string{
		"https://t.me/+" + want,
		"http://t.me/+" + want,
		"t.me/+" + want,
		"https://t.me/joinchat/" + want,
		"t.me/joinchat/" + want,
		"https://telegram.me/+" + want,
		"https://telegram.dog/+" + want,
		"www.t.me/+" + want,
		"tg://join?invite=" + want,
		"telegram://join?invite=" + want,
		"+" + want,
		want,
		// Trailing slash + query string.
		"https://t.me/+" + want + "/",
		"https://t.me/+" + want + "?via=share",
		"https://t.me/joinchat/" + want + "#fragment",
	}
	for _, in := range good {
		got, err := ParseInviteHash(in)
		if err != nil {
			t.Errorf("ParseInviteHash(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseInviteHash(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"",
		"https://t.me/publicchannel",     // public username, not an invite
		"https://t.me/+with spaces here", // invalid chars
		"https://example.com/foo",        // unrelated URL
		"abc",                            // too short
		"https://t.me/+",                 // empty after +
		"@@@",                            // invalid chars
	}
	for _, in := range bad {
		if got, err := ParseInviteHash(in); err == nil {
			t.Errorf("ParseInviteHash(%q) = %q, want error", in, got)
		}
	}
}
