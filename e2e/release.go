// Package e2e is the opt-in end-to-end suite for thefeed-server and its
// admin panel, behind the e2e build tag. On a disposable Linux host with
// systemd it installs locally built binaries with scripts/install.sh, drives
// the panel's HTTPS API through first run, restart, channels, answering,
// stop and start, update and rollback, and the login lockout, then
// uninstalls. Run it with `make e2e`; see the README, "End-to-end tests".
package e2e

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"net/http"
	"path"
	"strings"
)

// fakeRelease serves one release, tag, the way github.com lays one out for
// the panel's update (THEFEED_ADMIN_RELEASE_BASE_URL): HEAD
// <repo>/releases/latest redirects to <repo>/releases/tag/<tag>, and
// <repo>/releases/download/<tag>/<name> is each asset, plus SHA256SUMS over
// them. Any repo.
func fakeRelease(tag string, assets map[string][]byte) http.Handler {
	files := maps.Clone(assets)
	sums := ""
	for name, b := range assets {
		sums += fmt.Sprintf("%x  %s\n", sha256.Sum256(b), name)
	}
	files["SHA256SUMS"] = []byte(sums)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo, ok := strings.CutSuffix(r.URL.Path, "/releases/latest"); ok {
			http.Redirect(w, r, repo+"/releases/tag/"+tag, http.StatusFound)
			return
		}
		dir, name := path.Split(r.URL.Path)
		b, ok := files[name]
		if !ok || !strings.HasSuffix(dir, "/releases/download/"+tag+"/") {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	})
}
