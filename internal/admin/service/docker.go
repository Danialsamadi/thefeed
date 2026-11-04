package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/dockerapi"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// showContainer reads thefeed-server's container: running or not, since
// when, and whether Docker starts it again (restart policy unless-stopped).
func (m *mod) showContainer(ctx context.Context) (shown, error) {
	c, err := m.Docker.Inspect(ctx, m.Container)
	if err != nil {
		return shown{}, err
	}
	return shown{active: c.State.Running, boot: c.HostConfig.RestartPolicy.Name == "unless-stopped", started: c.State.StartedAt, shadowing: composeShadowing(c)}, nil
}

// composeShadowing lists the settings the container gets from its
// environment, such as THEFEED_DOMAIN, or its command, such as --listen:
// both beat thefeed.env. The data directory is never in thefeed.env.
func composeShadowing(c *dockerapi.Container) []string {
	out := []string{}
	for _, kv := range c.Config.Env {
		k, _, _ := strings.Cut(kv, "=")
		if slices.ContainsFunc(serverconfig.Settings, func(s serverconfig.Setting) bool { return s.Env == k && s.Flag != "data-dir" }) {
			out = append(out, k)
		}
	}
	uses, _ := shadowing(append([]string{c.Path}, c.Args...))
	for _, f := range uses {
		out = append(out, "--"+f.Flag)
	}
	return out
}

// updateContainer pulls tag's image, writes the tag into compose's .env so
// docker compose up -d keeps it, and recreates the container on it. If it
// isn't healthy in time, the container goes back to the image it ran and
// .env to what it said.
func (m *mod) updateContainer(ctx context.Context, name, tag, old string) error {
	c, err := m.Docker.Inspect(ctx, m.Container)
	if err != nil {
		return m.note(name, err)
	}
	repo := repository(c.Config.Image)
	if err := m.Docker.Pull(ctx, repo, tag); err != nil {
		return m.note(name, fmt.Errorf("Couldn't pull %s:%s: %v", repo, tag, err))
	}
	was, err := m.setTag(tag)
	if err != nil {
		return m.note(name, err)
	}
	m.edit(func(sv *saved) { sv.Previous = old })
	if err := m.recreate(ctx, c, repo+":"+tag); err == nil && m.healthy(ctx, tag) {
		m.line("Updated to "+tag, "")
		return m.note(name, nil)
	}
	// .env goes back even if the container can't, so docker compose up -d
	// brings back the version that worked.
	err = m.recreate(ctx, c, c.Config.Image)
	if werr := was(); err == nil {
		err = werr
	}
	return m.rolledBack(name, tag, old, err)
}

// rollbackContainer puts the container back on tag to, the version the last
// update replaced, and writes it into .env.
func (m *mod) rollbackContainer(ctx context.Context, name, to string) error {
	c, err := m.Docker.Inspect(ctx, m.Container)
	if err != nil {
		return m.note(name, err)
	}
	// An install that started on latest never pulled it by its tag.
	repo := repository(c.Config.Image)
	if err := m.Docker.Pull(ctx, repo, to); err != nil {
		return m.note(name, fmt.Errorf("Couldn't pull %s:%s: %v", repo, to, err))
	}
	if _, err := m.setTag(to); err != nil {
		return m.note(name, err)
	}
	m.edit(func(sv *saved) { sv.Previous, sv.Version = "", to })
	if err := m.recreate(ctx, c, repo+":"+to); err != nil {
		return m.note(name, err)
	}
	m.line("Rolled back to "+to, "")
	return m.note(name, nil)
}

// recreate puts the container, as inspected in c, on image: it's stopped
// and removed, then created like c and started. It may be gone already,
// when an earlier try failed after removing it.
func (m *mod) recreate(ctx context.Context, c *dockerapi.Container, image string) error {
	for _, step := range []func(context.Context, string) error{m.Docker.Stop, m.Docker.Remove} {
		var de *dockerapi.Error
		if err := step(ctx, m.Container); err != nil && !(errors.As(err, &de) && de.Status == http.StatusNotFound) {
			return err
		}
	}
	if err := m.Docker.Create(ctx, c, image); err != nil {
		return err
	}
	return m.Docker.Start(ctx, m.Container)
}

// repository is image without its tag or digest: ghcr.io/o/thefeed-server
// of ghcr.io/o/thefeed-server:v0.41.0. A registry's port isn't a tag.
func repository(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	return image
}

// setTag writes THEFEED_SERVER_TAG=tag into compose's .env, keeping its
// other lines, mode and owner. was writes back what it said before.
func (m *mod) setTag(tag string) (was func() error, err error) {
	data, hash, err := serverconfig.ReadFile(m.ComposeEnv)
	if err != nil {
		return nil, err
	}
	env := serverconfig.ParseEnv(data)
	if err := env.Set("THEFEED_SERVER_TAG", tag); err != nil {
		return nil, err
	}
	b := env.Bytes()
	if err := serverconfig.WriteFile(m.ComposeEnv, b, hash); err != nil {
		return nil, fmt.Errorf("Couldn't write the tag into %s: %v", m.ComposeEnv, err)
	}
	return func() error { return serverconfig.WriteFile(m.ComposeEnv, data, serverconfig.Hash(b)) }, nil
}

// noUnit is fix-unit's answer in Docker mode.
var noUnit = &refused{http.StatusBadRequest, admin.FieldError{Message: "thefeed-server runs in Docker, so there's no unit to fix. " +
	"Remove the listed settings from the server's environment and command in docker-compose.yml, then run docker compose up -d."}}

// toContainer does to the container what systemctl verb does to the unit.
// Docker starts a container again at boot when its restart policy is
// unless-stopped.
func (m *mod) toContainer(ctx context.Context, verb string) error {
	switch verb {
	case "restart":
		return m.Docker.Restart(ctx, m.Container)
	case "stop":
		return m.Docker.Stop(ctx, m.Container)
	case "start":
		return m.Docker.Start(ctx, m.Container)
	case "enable":
		return m.Docker.SetRestart(ctx, m.Container, "unless-stopped")
	default: // disable
		return m.Docker.SetRestart(ctx, m.Container, "no")
	}
}
