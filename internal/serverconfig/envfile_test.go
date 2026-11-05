package serverconfig

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const sampleEnv = "# thefeed settings\n" +
	"THEFEED_DOMAIN=t.example.com\n" +
	"\n" +
	"# hand-added, unknown to the registry\n" +
	"MY_OWN_THING=keep me\n" +
	"export THEFEED_MSG_LIMIT=15\r\n" +
	"THEFEED_GITHUB_RELAY_BRANCH=\"main\"\n" +
	"THEFEED_PADDING=32\n" +
	"THEFEED_PADDING=40" // no final newline, and a duplicate

func TestEnvRoundTripsByteIdentical(t *testing.T) {
	for _, in := range []string{sampleEnv, "", "\n", "A=1", "A=1\n\n\n"} {
		if got := string(ParseEnv([]byte(in)).Bytes()); got != in {
			t.Errorf("round trip changed %q into %q", in, got)
		}
	}
}

func TestEnvGet(t *testing.T) {
	e := ParseEnv([]byte(sampleEnv))
	for key, want := range map[string]string{
		"THEFEED_DOMAIN": "t.example.com", "MY_OWN_THING": "keep me", "THEFEED_MSG_LIMIT": "15",
		"THEFEED_GITHUB_RELAY_BRANCH": "main", "THEFEED_PADDING": "40",
	} {
		if got, ok := e.Get(key); !ok || got != want {
			t.Errorf("Get(%s) = %q, %v; want %q", key, got, ok, want)
		}
	}
	if _, ok := e.Get("THEFEED_KEY"); ok {
		t.Error("Get found a key that isn't there")
	}
}

func TestEnvSetenvRealEnvironmentWins(t *testing.T) {
	t.Setenv("THEFEED_DOMAIN", "real.example.com")
	t.Setenv("THEFEED_MSG_LIMIT", "") // set, though empty: still the real environment's
	for _, k := range []string{"MY_OWN_THING", "THEFEED_GITHUB_RELAY_BRANCH", "THEFEED_PADDING"} {
		t.Setenv(k, "") // restored to unset afterwards
		os.Unsetenv(k)
	}

	ParseEnv([]byte(sampleEnv)).Setenv()

	for key, want := range map[string]string{
		"THEFEED_DOMAIN": "real.example.com", "THEFEED_MSG_LIMIT": "",
		"MY_OWN_THING": "keep me", "THEFEED_GITHUB_RELAY_BRANCH": "main", "THEFEED_PADDING": "40",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestEnvSetChangesOnlyThatLine(t *testing.T) {
	for _, c := range []struct{ key, value, want string }{
		{"THEFEED_DOMAIN", "t2.example.com", "# thefeed settings\nTHEFEED_DOMAIN=t2.example.com\n\n# hand-added, unknown to the registry\nMY_OWN_THING=keep me\nexport THEFEED_MSG_LIMIT=15\r\nTHEFEED_GITHUB_RELAY_BRANCH=\"main\"\nTHEFEED_PADDING=32\nTHEFEED_PADDING=40"},
		{"THEFEED_MSG_LIMIT", "20", "# thefeed settings\nTHEFEED_DOMAIN=t.example.com\n\n# hand-added, unknown to the registry\nMY_OWN_THING=keep me\nexport THEFEED_MSG_LIMIT=20\r\nTHEFEED_GITHUB_RELAY_BRANCH=\"main\"\nTHEFEED_PADDING=32\nTHEFEED_PADDING=40"},
		{"THEFEED_GITHUB_RELAY_BRANCH", "dev", "# thefeed settings\nTHEFEED_DOMAIN=t.example.com\n\n# hand-added, unknown to the registry\nMY_OWN_THING=keep me\nexport THEFEED_MSG_LIMIT=15\r\nTHEFEED_GITHUB_RELAY_BRANCH=\"dev\"\nTHEFEED_PADDING=32\nTHEFEED_PADDING=40"},
		// Every copy changes, so systemd (last wins) and the server (first wins) agree.
		{"THEFEED_PADDING", "0", "# thefeed settings\nTHEFEED_DOMAIN=t.example.com\n\n# hand-added, unknown to the registry\nMY_OWN_THING=keep me\nexport THEFEED_MSG_LIMIT=15\r\nTHEFEED_GITHUB_RELAY_BRANCH=\"main\"\nTHEFEED_PADDING=0\nTHEFEED_PADDING=0"},
		{"THEFEED_DEBUG", "1", sampleEnv + "\nTHEFEED_DEBUG=1\n"},
	} {
		e := ParseEnv([]byte(sampleEnv))
		if err := e.Set(c.key, c.value); err != nil {
			t.Fatal(err)
		}
		if got := string(e.Bytes()); got != c.want {
			t.Errorf("Set(%s=%s):\n got %q\nwant %q", c.key, c.value, got, c.want)
		}
	}

	e := ParseEnv(nil)
	_ = e.Set("THEFEED_DEBUG", "1")
	if got := string(e.Bytes()); got != "THEFEED_DEBUG=1\n" {
		t.Errorf("Set on an empty file = %q", got)
	}
}

func TestEnvSetRefusesValuesThatNeedQuoting(t *testing.T) {
	for _, v := range []string{"a b", "x\nTHEFEED_ALLOW_MANAGE=1", `"q"`, "it's", "$HOME", "`id`", `a\b`} {
		e := ParseEnv([]byte(sampleEnv))
		if err := e.Set("THEFEED_DOMAIN", v); !errors.Is(err, ErrUnsafeValue) {
			t.Errorf("Set(%q) = %v, want ErrUnsafeValue", v, err)
		}
		if string(e.Bytes()) != sampleEnv {
			t.Errorf("a refused Set(%q) still changed the file", v)
		}
	}
}

func TestWriteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thefeed.env")

	// A missing file reads as empty, and its hash lets the first write create it.
	data, h, err := ReadFile(path)
	if err != nil || len(data) != 0 || h != Hash(nil) {
		t.Fatalf("ReadFile(missing) = %q, %s, %v", data, h, err)
	}
	if err := WriteFile(path, []byte("A=1\n"), h); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("new file mode = %v, want 0600", fi.Mode().Perm())
	}

	// The mode is kept.
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	_, h, _ = ReadFile(path)
	if err := WriteFile(path, []byte("A=2\n"), h); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o640 {
		t.Errorf("mode after write = %v, want 0640", fi.Mode().Perm())
	}

	// Someone edits it over SSH: a write based on the old content is refused,
	// and their edit stays.
	if err := os.WriteFile(path, []byte("A=3\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("A=4\n"), h); !errors.Is(err, ErrChangedElsewhere) {
		t.Fatalf("stale write = %v, want ErrChangedElsewhere", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "A=3\n" {
		t.Errorf("a refused write changed the file to %q", got)
	}

	// No temporary files are left behind.
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("directory has %d entries, want just the file", len(entries))
	}
}
