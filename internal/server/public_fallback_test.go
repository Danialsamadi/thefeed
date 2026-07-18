package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newFallbackReader(bases ...string) *PublicReader {
	return &PublicReader{
		client:     &http.Client{Timeout: 5 * time.Second},
		pageClient: newPageClient(),
		baseURLs:   bases,
	}
}

// A domain that errors is skipped, the next one is used, and the working
// domain is remembered so the dead one isn't re-probed on the next call.
func TestFetchPageBodyFallsThroughAndSticks(t *testing.T) {
	var downHits, upHits int32
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&downHits, 1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upHits, 1)
		_, _ = w.Write([]byte(`<div class="tgme_widget_message">hi</div>`))
	}))
	defer up.Close()

	pr := newFallbackReader(down.URL+"/s", up.URL+"/s")

	body, err := pr.fetchPageBody(context.Background(), "chan")
	if err != nil {
		t.Fatalf("first fetch: unexpected error: %v", err)
	}
	if !strings.Contains(string(body), "tgme_widget_message") {
		t.Fatalf("first fetch: wrong body %q", body)
	}
	if got := atomic.LoadInt32(&downHits); got != 1 {
		t.Fatalf("first fetch: down base hit %d times, want 1", got)
	}

	// Second fetch must skip the dead base entirely (sticky preference).
	if _, err := pr.fetchPageBody(context.Background(), "chan2"); err != nil {
		t.Fatalf("second fetch: unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&downHits); got != 1 {
		t.Fatalf("sticky failed: down base hit %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&upHits); got != 2 {
		t.Fatalf("up base hit %d times, want 2", got)
	}
}

// A 2xx response that is not Telegram markup must be rejected and the next
// domain tried, not handed to the parser.
func TestFetchPageBodyRejectsNonTelegram200(t *testing.T) {
	parked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>domain for sale</body></html>`))
	}))
	defer parked.Close()
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<div class="tgme_channel_info">x</div>`))
	}))
	defer real.Close()

	pr := newFallbackReader(parked.URL+"/s", real.URL+"/s")
	body, err := pr.fetchPageBody(context.Background(), "chan")
	if err != nil {
		t.Fatalf("expected fall-through to real base, got error: %v", err)
	}
	if !strings.Contains(string(body), "tgme_channel_info") {
		t.Fatalf("did not fall through past the parked domain: %q", body)
	}
}

// A base that redirects cross-host (the "channel not found" bounce to
// t.me) must not be followed — the redirect is treated as a failure and
// the next base is tried, without ever hitting the redirect target.
func TestFetchPageBodyStopsCrossHostRedirect(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		_, _ = w.Write([]byte(`<div class="tgme_widget_message">should not be reached</div>`))
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusFound) // cross-host (different port)
	}))
	defer redir.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<div class="tgme_channel_info">ok</div>`))
	}))
	defer good.Close()

	pr := newFallbackReader(redir.URL+"/s", good.URL+"/s")
	body, err := pr.fetchPageBody(context.Background(), "chan")
	if err != nil {
		t.Fatalf("expected fall-through to the good base, got error: %v", err)
	}
	if !strings.Contains(string(body), "tgme_channel_info") {
		t.Fatalf("did not fall through past the redirecting base: %q", body)
	}
	if got := atomic.LoadInt32(&targetHits); got != 0 {
		t.Fatalf("cross-host redirect was followed: target hit %d times", got)
	}
}

// When every domain fails, the returned error names all of them.
func TestFetchPageBodyJoinsAllErrors(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	parked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not telegram"))
	}))
	defer parked.Close()

	pr := newFallbackReader(bad.URL+"/s", parked.URL+"/s")
	_, err := pr.fetchPageBody(context.Background(), "chan")
	if err == nil {
		t.Fatal("expected an error when every base fails")
	}
	msg := err.Error()
	if !strings.Contains(msg, bad.URL) || !strings.Contains(msg, parked.URL) {
		t.Fatalf("joined error should name both bases, got: %q", msg)
	}
}
