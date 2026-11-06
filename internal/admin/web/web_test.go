package web_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/web"
)

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	app, err := admin.New(admin.Config{AdminDir: t.TempDir()}, web.Module)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	return app.Handler()
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://panel.test"+path, nil))
	return w
}

// The shell is public: it loads before sign-in and decides on the page which
// screen to show, while the API behind it still wants a session.
func TestShellIsServedBeforeSignIn(t *testing.T) {
	h := newHandler(t)
	w := get(h, "/")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET / = %d %q, want 200 text/html", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "thefeed admin") {
		t.Fatalf("GET / body doesn't look like the panel shell:\n%s", w.Body)
	}
	// Nothing from other sites: the browser refuses anything not from the panel.
	if csp := w.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "default-src 'self'") {
		t.Fatalf("GET / Content-Security-Policy = %q, want default-src 'self'", csp)
	}
	if w := get(h, "/api/auth/session"); w.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/auth/session without a session = %d, want 401", w.Code)
	}
}

// Everything the shell names comes from the embedded files, with a type the
// browser accepts under nosniff, and every page file is loaded by the shell:
// adding a page is one file in static/js/pages plus one script tag.
func TestShellAssetsAndPagesAreServed(t *testing.T) {
	h := newHandler(t)
	shell := get(h, "/").Body.String()
	refs := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`).FindAllStringSubmatch(shell, -1) {
		refs[m[1]] = true
	}
	want := []string{"/static/css/admin.css", "/static/js/util.js", "/static/js/shell.js"}
	pages, err := os.ReadDir("static/js/pages")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pages {
		want = append(want, "/static/js/pages/"+p.Name())
	}
	for _, p := range want {
		if !refs[p] {
			t.Errorf("the shell doesn't load %s", p)
		}
	}
	types := map[string]string{".css": "text/css", ".js": "text/javascript", ".woff2": ""}
	for p := range refs {
		w := get(h, p)
		ct, ok := types[path.Ext(p)]
		if w.Code != http.StatusOK || !ok || !strings.HasPrefix(w.Header().Get("Content-Type"), ct) {
			t.Errorf("GET %s = %d %q, want 200 %s", p, w.Code, w.Header().Get("Content-Type"), ct)
		}
	}
	// After an update, a browser must not keep showing the old page: the
	// shell and every asset are checked with the panel each time.
	for p := range refs {
		if cc := get(h, p).Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s: Cache-Control %q, want no-cache", p, cc)
		}
	}
	if cc := get(h, "/").Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("GET /: Cache-Control %q, want no-cache", cc)
	}
}

// The shell is served at / only. A mistyped API path is a 404, not the shell:
// the page parses API answers as JSON and would choke on HTML. Nor does the
// asset directory hand out the shell (without its CSP) or list itself.
func TestUnknownPathsAreNotTheShell(t *testing.T) {
	h := newHandler(t)
	for _, p := range []string{"/api/nope", "/control", "/static/", "/static/js/", "/static/index.html"} {
		if w := get(h, p); w.Code == http.StatusOK {
			t.Errorf("GET %s = 200 %q, want no page", p, w.Header().Get("Content-Type"))
		}
	}
}
