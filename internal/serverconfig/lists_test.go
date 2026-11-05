package serverconfig

import (
	"strings"
	"testing"
)

func TestListEntries(t *testing.T) {
	const (
		chErr  = "Channel names are 5 to 32 letters, digits or underscores, starting with a letter."
		xErr   = "X handles are 1 to 15 letters, digits or underscores."
		inErr  = "Type a host like nitter.example.com, or a link like https://nitter.example.com."
		invErr = "That isn't an invite link. Use t.me/+…, t.me/joinchat/… or tg://join?invite=…."
		isInv  = "That's an invite link. Add it under Private channels."
	)
	for _, c := range []struct {
		codec         *Codec
		in, want, err string
	}{
		{Channels, "daily_brief", "daily_brief", ""},
		{Channels, "@daily_brief", "daily_brief", ""},
		{Channels, "https://t.me/daily_brief", "daily_brief", ""},
		{Channels, "t.me/s/daily_brief?before=10", "daily_brief", ""},
		{Channels, "telegram.me/Daily_Brief/", "Daily_Brief", ""},
		{Channels, "abcd", "", chErr},
		{Channels, "1daily", "", chErr},
		{Channels, strings.Repeat("a", 33), "", chErr},
		{Channels, "daily-brief", "", chErr},
		{Channels, "", "", typeFirst},
		{Channels, "https://t.me/+aBcDeF123-_", "", isInv},
		{Channels, "t.me/joinchat/aBcDeF123", "", isInv},
		{Channels, "tg://join?invite=aBcDeF123", "", isInv},

		{XAccounts, "space_daily", "space_daily", ""},
		{XAccounts, "@a", "a", ""},
		{XAccounts, "https://x.com/tech_wire", "tech_wire", ""},
		{XAccounts, "twitter.com/tech_wire/status/1", "tech_wire", ""},
		{XAccounts, "www.twitter.com/tech_wire", "tech_wire", ""},
		{XAccounts, "sixteen_letters_", "", xErr},
		{XAccounts, "dot.name", "", xErr},

		{PrivateChannels, "t.me/+aBcDeF123-_", "https://t.me/+aBcDeF123-_", ""},
		{PrivateChannels, "https://t.me/joinchat/aBcDeF123", "https://t.me/+aBcDeF123", ""},
		{PrivateChannels, "tg://join?invite=aBcDeF123", "https://t.me/+aBcDeF123", ""},
		{PrivateChannels, "https://t.me/daily_brief", "", invErr},

		{Instances, "nitter.example.com", "https://nitter.example.com", ""},
		{Instances, "https://Nitter.Example.com/", "https://nitter.example.com", ""},
		{Instances, "http://nitter.net", "http://nitter.net", ""},
		{Instances, "http://192.0.2.7:8080", "http://192.0.2.7:8080", ""},
		{Instances, "ftp://nitter.net", "", inErr},
		{Instances, "https://nitter.net/search", "", inErr},
		{Instances, "nitter .net", "", inErr},
		{Instances, "nit!ter.example.com", "", inErr}, // a URL, but not a hostname
		{Instances, "https://nitter.net:http", "", inErr},
	} {
		l := c.codec.Parse(nil)
		got, err := l.Add(c.in)
		gotErr := ""
		if err != nil {
			gotErr = err.Error()
		}
		if got != c.want || gotErr != c.err {
			t.Errorf("Add(%q) = %q, %q; want %q, %q", c.in, got, gotErr, c.want, c.err)
		}
	}
}

func TestListRefusesDuplicates(t *testing.T) {
	for _, c := range []struct {
		codec    *Codec
		file, in string
		want     string
	}{
		{Channels, "@Daily_Brief\n", "t.me/daily_brief", "@daily_brief is already on the list."},
		{XAccounts, "Tech_Wire\n", "@tech_wire", "@tech_wire is already on the list."},
		{PrivateChannels, "tg://join?invite=aBcDeF123\n", "t.me/+aBcDeF123", "That invite link is already on the list."},
		{Instances, "https://nitter.net,http://nitter.net", "NITTER.NET", "https://nitter.net is already on the list."},
	} {
		l := c.codec.Parse([]byte(c.file))
		if _, err := l.Add(c.in); err == nil || err.Error() != c.want {
			t.Errorf("Add(%q) to %q = %v, want %q", c.in, c.file, err, c.want)
		}
	}

	// Invite hashes are case-sensitive: these are two channels.
	l := PrivateChannels.Parse([]byte("t.me/+aBcDeF123\n"))
	if _, err := l.Add("t.me/+ABCDEF123"); err != nil {
		t.Error(err)
	}

	// Renaming an entry to itself, in another case, is not a duplicate.
	l = Channels.Parse([]byte("@daily_brief\n@world_desk\n"))
	if got, err := l.Rename(0, "Daily_Brief"); err != nil || got != "Daily_Brief" {
		t.Errorf("Rename to itself = %q, %v", got, err)
	}
	if _, err := l.Rename(0, "world_desk"); err == nil {
		t.Error("Rename onto another entry was allowed")
	}
	if _, err := l.Rename(2, "tech_digest"); err == nil || err.Error() != "That entry is no longer on the list." {
		t.Errorf("Rename past the end = %v", err)
	}
}

func TestListRoundTripKeepsHeader(t *testing.T) {
	// Only the leading comment block is a header; later comments are not kept,
	// as when the server's own DNS admin command rewrites the file.
	file := "# my own header\n# Lines starting with # are comments\n\n@daily_brief\n# @gone_channel\n@ab\nworld_desk\n"
	l := Channels.Parse([]byte(file))
	if len(l.Items) != 3 || l.Items[1] != "ab" {
		t.Fatalf("items = %q; an old entry the rules now refuse must still be listed", l.Items)
	}
	if _, err := l.Add("tech_digest"); err != nil {
		t.Fatal(err)
	}
	want := "# my own header\n# Lines starting with # are comments\n@daily_brief\n@ab\n@world_desk\n@tech_digest\n"
	if got := string(l.Bytes()); got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}

	// A file without a header gets the installer's.
	l = XAccounts.Parse([]byte("@space_daily\n"))
	if got := string(l.Bytes()); got != "# X usernames (one per line, without @)\n# Lines starting with # are comments\nspace_daily\n" {
		t.Errorf("x_accounts.txt = %q", got)
	}

	l = PrivateChannels.Parse([]byte("# Telegram private-channel invite links (one per line)\nt.me/joinchat/aBcDeF123\n"))
	if got := string(l.Bytes()); got != "# Telegram private-channel invite links (one per line)\nt.me/joinchat/aBcDeF123\n" {
		t.Errorf("private_channels.txt = %q", got)
	}

	l = Instances.Parse([]byte(" https://nitter.net, http://nitter.net ,"))
	if _, err := l.Add("xcancel.com"); err != nil {
		t.Fatal(err)
	}
	if got := string(l.Bytes()); got != "https://nitter.net,http://nitter.net,https://xcancel.com" {
		t.Errorf("instances = %q", got)
	}
}

func TestLooksLikeInvite(t *testing.T) {
	for in, want := range map[string]bool{
		"https://t.me/+aBcDeF123": true, "t.me/joinchat/aBcDeF123": true, "tg://join?invite=aBcDeF123": true,
		"+aBcDeF123": true, "daily_brief": false, "@daily_brief": false, "https://t.me/daily_brief": false,
	} {
		if got := LooksLikeInvite(in); got != want {
			t.Errorf("LooksLikeInvite(%q) = %v", in, got)
		}
	}
}
