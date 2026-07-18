package telemirror

import (
	"net/http"
	"testing"
)

func req(t *testing.T, rawurl string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, rawurl, nil)
	if err != nil {
		t.Fatalf("bad url %q: %v", rawurl, err)
	}
	return r
}

// The widget redirect policy must follow only a same-host redirect that
// keeps the /s/ preview path, and stop (→ not-found) for everything else:
// the join-page bounce (drops /s/) and the cross-host bounce to t.me.
func TestFollowPreviewRedirect(t *testing.T) {
	orig := []*http.Request{req(t, "https://telegram-me.translate.goog/s/durov?_x_tr_sl=auto")}

	cases := []struct {
		name   string
		target string
		follow bool
	}{
		{"same host, keeps /s/ (canonical move)", "https://telegram-me.translate.goog/s/durov2?_x_tr_sl=auto", true},
		{"same host, drops /s/ (join page)", "https://telegram-me.translate.goog/IranIntl?_x_tr_sl=auto", false},
		{"cross host to dead t-me proxy", "https://t-me.translate.goog/IranIntl?_x_tr_sl=auto", false},
		{"cross host to real t.me", "https://t.me/IranIntl", false},
	}
	for _, c := range cases {
		err := followPreviewRedirect(req(t, c.target), orig)
		follow := err == nil
		if follow != c.follow {
			t.Errorf("%s: follow=%v, want %v (err=%v)", c.name, follow, c.follow, err)
		}
		if !follow && err != http.ErrUseLastResponse {
			t.Errorf("%s: expected ErrUseLastResponse, got %v", c.name, err)
		}
	}
}
