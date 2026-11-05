package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNitter serves any account's RSS, or HTTP 429 while limited, and
// counts the requests it gets.
type fakeNitter struct {
	*httptest.Server
	limited bool

	mu       sync.Mutex
	hits     int
	accounts map[string]bool // whose RSS it was asked for
}

func newFakeNitter(t *testing.T, limited bool) *fakeNitter {
	f := &fakeNitter{limited: limited, accounts: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits++
		if account, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/rss"); ok {
			f.accounts[account] = true
		}
		f.mu.Unlock()
		if f.limited {
			w.Header().Set("Retry-After", "600")
			http.Error(w, "Instance has been rate limited.", http.StatusTooManyRequests)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/pic/") {
			w.Write([]byte("jpeg"))
			return
		}
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss><channel><title>x</title><image><url>/pic/a.jpg</url></image>
  <item>
    <title>post</title>
    <link>https://x.com/a/status/1930000000000000001</link>
    <guid>https://x.com/a/status/1930000000000000001</guid>
    <description><![CDATA[<p>Hello</p>]]></description>
    <pubDate>Mon, 30 Mar 2026 04:45:00 +0000</pubDate>
  </item>
</channel></rss>`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeNitter) count() (hits, accounts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits, len(f.accounts)
}

func newLoadReader(fakes ...*fakeNitter) *XPublicReader {
	xr := NewXPublicReader([]string{"one", "two", "three"}, NewFeed(nil), 15, 1, "")
	xr.instances = nil
	for _, f := range fakes {
		xr.instances = append(xr.instances, f.URL)
	}
	return xr
}

// An instance that answers 429 gets one request a fetch cycle, not one per
// account, and rests: the check reports it resting without asking it again.
func TestXRateLimitedInstanceRests(t *testing.T) {
	limited, a, b := newFakeNitter(t, true), newFakeNitter(t, false), newFakeNitter(t, false)
	xr := newLoadReader(limited, a, b)

	xr.fetchAll(context.Background())
	if hits, _ := limited.count(); hits != 1 {
		t.Errorf("the rate-limited instance got %d requests in one cycle, want 1", hits)
	}
	res := xr.check(context.Background())
	if hits, _ := limited.count(); hits != 1 {
		t.Errorf("the check asked the resting instance again: %d requests, want 1", hits)
	}
	if !strings.Contains(res.Detail, limited.URL+" rate limited") {
		t.Errorf("check detail = %q, want %s rate limited", res.Detail, limited.URL)
	}
	st := xr.controlStats()
	if in := st.Instances[0]; in.RestUntil.IsZero() {
		t.Errorf("the status gives the limited instance no rest: %+v", in)
	}
}

// Healthy instances share the accounts instead of the first serving all.
func TestXInstancesShareTheAccounts(t *testing.T) {
	a, b, c := newFakeNitter(t, false), newFakeNitter(t, false), newFakeNitter(t, false)
	xr := newLoadReader(a, b, c)

	xr.fetchAll(context.Background())
	for i, f := range []*fakeNitter{a, b, c} {
		if _, accounts := f.count(); accounts != 1 {
			t.Errorf("instance %d was asked for %d accounts, want 1 of the 3", i, accounts)
		}
	}
}

// Retry-After is seconds or a date; missing, a 429 rests 10 minutes, and
// any rest is kept between a minute and an hour.
func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 5, 22, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"":                              10 * time.Minute,
		"120":                           2 * time.Minute,
		"Thu, 05 Nov 2026 22:30:00 GMT": 30 * time.Minute,
		"5":                             time.Minute,
		"86400":                         time.Hour,
		"soon":                          10 * time.Minute,
	} {
		if got := retryAfter(v, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", v, got, want)
		}
	}
}
