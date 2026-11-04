package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/update"
)

// floor is the oldest thefeed-server the panel installs. Older ones don't
// read THEFEED_NO_TELEGRAM or THEFEED_LISTEN from thefeed.env, so they run
// badly under the installer's unit, which passes no settings as flags.
const floor = "v0.40.1-rc0"

// tagRE is a release tag. It goes into download URLs, so nothing else does.
var tagRE = regexp.MustCompile(`^v?[0-9]+(\.[0-9]+)*(-[0-9A-Za-z.]+)?$`)

// maxBinary caps a download; thefeed-server is about 30 MB.
const maxBinary = 256 << 20

func (m *mod) update(ctx context.Context, tag string) error {
	if !tagRE.MatchString(tag) {
		return &refused{http.StatusBadRequest, admin.FieldError{Field: "version", Message: "That isn't a release version, such as v0.41.1."}}
	}
	if update.IsNewer(floor, tag) {
		return &refused{http.StatusBadRequest, admin.FieldError{Field: "version", Message: tag + " is older than " + floor + ", the oldest version that reads every setting from thefeed.env."}}
	}
	name, old := "Update to "+tag, m.version(ctx)
	if m.Docker != nil {
		return m.updateContainer(ctx, name, tag, old)
	}
	if err := m.fetch(ctx, m.source(m.load()), tag); err != nil {
		return m.note(name, err)
	}
	// The current binary stays as .previous (a second name for the same
	// file), then the new one takes its place in one rename: there's always
	// a binary to start.
	prev := m.Binary + ".previous"
	os.Remove(prev)
	if err := os.Link(m.Binary, prev); err != nil {
		os.Remove(m.Binary + ".new")
		return m.note(name, fmt.Errorf("Couldn't keep the current version as %s: %v", prev, err))
	}
	if err := os.Rename(m.Binary+".new", m.Binary); err != nil {
		return m.note(name, err)
	}
	m.edit(func(sv *saved) { sv.Previous = old })
	if _, err := m.Systemd.Run(ctx, "restart", unit); err == nil && m.healthy(ctx, tag) {
		m.line("Updated to "+tag, "")
		return m.note(name, nil)
	}

	// Not healthy: put the old one back.
	err := os.Rename(prev, m.Binary)
	if err == nil {
		_, err = m.Systemd.Run(ctx, "restart", unit)
	}
	return m.rolledBack(name, tag, old, err)
}

// rolledBack ends an update to tag that wasn't healthy, once old is back;
// err is putting it back failing.
func (m *mod) rolledBack(name, tag, old string, err error) error {
	if err != nil {
		return m.note(name, fmt.Errorf("%s didn't answer healthy within %d seconds, and putting %s back failed: %v", tag, healthWait/time.Second, old, err))
	}
	m.edit(func(sv *saved) { sv.Previous = "" })
	msg := fmt.Sprintf("%s didn't answer healthy within %d seconds, so the panel rolled back to %s", tag, healthWait/time.Second, old)
	m.line(msg, "")
	return m.note(name, errors.New(msg+"."))
}

// rollback puts the version kept by the last update back, and restarts.
func (m *mod) rollback(ctx context.Context) error {
	to := m.load().Previous
	if to == "" {
		return &refused{http.StatusConflict, admin.FieldError{Message: "There's no previous version to roll back to."}}
	}
	if update.IsNewer(floor, to) {
		return &refused{http.StatusConflict, admin.FieldError{Message: to + " is older than " + floor + ", the oldest version that reads every setting from thefeed.env, so the panel won't go back to it."}}
	}
	name := "Roll back to " + to
	if m.Docker != nil {
		return m.rollbackContainer(ctx, name, to)
	}
	if err := os.Rename(m.Binary+".previous", m.Binary); err != nil {
		return m.note(name, err)
	}
	m.edit(func(sv *saved) { sv.Previous, sv.Version = "", to })
	if _, err := m.Systemd.Run(ctx, "restart", unit); err != nil {
		return m.note(name, err)
	}
	m.line("Rolled back to "+to, "")
	return m.note(name, nil)
}

var repoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// setSource sets the GitHub repo updates come from; "" is the default.
func (m *mod) setSource(repo string) error {
	if repo != "" && !repoRE.MatchString(repo) {
		return &refused{http.StatusBadRequest, admin.FieldError{Field: "repo", Message: "Write it as owner/repo, such as sartoopjj/thefeed."}}
	}
	if err := m.edit(func(sv *saved) { sv.Repo = repo }); err != nil {
		return err
	}
	return m.d.Record(admin.Action{Action: "Release source", Outcome: m.source(saved{Repo: repo})})
}

// healthWait is how long a new version has to answer /status as itself.
const healthWait = 60 * time.Second

// healthy waits for the server to answer /status as version.
func (m *mod) healthy(ctx context.Context, version string) bool {
	deadline := m.d.Clock.Now().Add(healthWait)
	for {
		if st, err := m.status(ctx); err == nil && st.Server.Version == version {
			return true
		}
		if !m.d.Clock.Now().Before(deadline) {
			return false
		}
		m.Sleep(ctx, 2*time.Second)
	}
}

// version is the installed version: as it runs now, else as last seen.
func (m *mod) version(ctx context.Context) string {
	if st, err := m.status(ctx); err == nil {
		return st.Server.Version
	}
	return m.load().Version
}

// fetch downloads tag's thefeed-server for this machine next to the
// installed one, as <binary>.new, if it matches the release's SHA256SUMS.
func (m *mod) fetch(ctx context.Context, repo, tag string) error {
	asset := "thefeed-server-linux-" + runtime.GOARCH
	sums, err := m.Releases.Download(ctx, repo, tag, "SHA256SUMS")
	if err != nil {
		return fmt.Errorf("Couldn't download SHA256SUMS of %s from %s: %v", tag, repo, err)
	}
	list, err := io.ReadAll(io.LimitReader(sums, 1<<20))
	sums.Close()
	if err != nil {
		return fmt.Errorf("Couldn't download SHA256SUMS of %s from %s: %v", tag, repo, err)
	}
	want := ""
	for _, l := range strings.Split(string(list), "\n") {
		if f := strings.Fields(l); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("SHA256SUMS of %s has no line for %s, so nothing was installed.", tag, asset)
	}

	body, err := m.Releases.Download(ctx, repo, tag, asset)
	if err != nil {
		return fmt.Errorf("Couldn't download %s of %s from %s: %v", asset, tag, repo, err)
	}
	defer body.Close()
	tmp := m.Binary + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(body, maxBinary))
	if err == nil {
		err = f.Chmod(0o755)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		err = fmt.Errorf("The download of %s doesn't match its line in SHA256SUMS, so nothing was installed.", asset)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
