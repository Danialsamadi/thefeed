package e2e

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/sartoopjj/thefeed/internal/update"
)

// The suite's fake release has to be laid out the way the panel's update
// reads THEFEED_ADMIN_RELEASE_BASE_URL. This runs without the e2e tag, so a
// layout the panel can't read fails here and not only on a Linux host.
func TestFakeReleaseReadsLikeGitHub(t *testing.T) {
	srv := httptest.NewServer(fakeRelease("v0.99.0", map[string][]byte{"thefeed-server-linux-amd64": []byte("binary")}))
	defer srv.Close()
	ctx := context.Background()

	if tag, err := update.LatestTag(ctx, srv.URL, "sartoopjj/thefeed"); err != nil || tag != "v0.99.0" {
		t.Errorf("latest = %q, %v; want v0.99.0", tag, err)
	}
	for name, want := range map[string]string{
		"SHA256SUMS":                 "9a3a45d01531a20e89ac6ae10b0b0beb0492acd7216a368aa062d1a5fecaf9cd  thefeed-server-linux-amd64\n",
		"thefeed-server-linux-amd64": "binary",
	} {
		body, err := update.OpenAsset(ctx, srv.URL, "sartoopjj/thefeed", "v0.99.0", name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got, _ := io.ReadAll(body)
		body.Close()
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if body, err := update.OpenAsset(ctx, srv.URL, "sartoopjj/thefeed", "v0.98.0", "SHA256SUMS"); err == nil {
		body.Close()
		t.Error("another tag's SHA256SUMS was served")
	}
}
