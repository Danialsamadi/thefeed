package service

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// unitFile is thefeed-server's unit file, with its ExecStart= command split
// into words.
//
// ponytail: words split on spaces, with no systemd quoting, as install.sh
// reads them; a quoted value with a space is refused when it's moved.
type unitFile struct {
	data     []byte
	lines    []string
	from, to int // the lines ExecStart= spans, with its \ continuations
	words    []string
}

func readUnit(path string) (u unitFile, err error) {
	u.data, err = os.ReadFile(path)
	u.lines = strings.Split(string(u.data), "\n")
	for i, l := range u.lines {
		cmd, ok := strings.CutPrefix(strings.TrimSpace(l), "ExecStart=")
		if !ok {
			continue
		}
		j := i
		for strings.HasSuffix(cmd, `\`) && j+1 < len(u.lines) {
			j++
			cmd = strings.TrimSuffix(cmd, `\`) + " " + u.lines[j]
		}
		u.from, u.to, u.words = i, j+1, strings.Fields(strings.TrimSuffix(cmd, `\`))
		break
	}
	return u, err
}

// flagUse is a setting the unit passes as a flag, which beats thefeed.env.
type flagUse struct {
	serverconfig.Setting
	value string
}

// shadowing finds the settings the command passes as flags, and the words
// left without them. --data-dir stays: the installer's unit passes it. A
// value with a $ is systemd's, read from thefeed.env. Flags the registry
// doesn't know aren't settings and stay.
func shadowing(words []string) (uses []flagUse, rest []string) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		name, val, hasVal := strings.Cut(strings.TrimLeft(w, "-"), "=")
		s, ok := byFlag(name)
		if i == 0 || !strings.HasPrefix(w, "-") || !ok || s.Flag == "data-dir" {
			rest = append(rest, w)
			continue
		}
		n := 1
		switch {
		case hasVal:
		case s.Type == serverconfig.TypeBool:
			val = "true"
		case i+1 < len(words):
			val, n = words[i+1], 2
		}
		if strings.Contains(val, "$") {
			rest = append(rest, words[i:i+n]...)
		} else {
			uses = append(uses, flagUse{s, val})
		}
		i += n - 1
	}
	return uses, rest
}

func byFlag(name string) (serverconfig.Setting, bool) {
	for _, s := range serverconfig.Settings {
		if s.Flag == name {
			return s, true
		}
	}
	return serverconfig.Setting{}, false
}

// fixUnit moves the settings the unit passes as flags into thefeed.env, with
// the values the server runs with, and leaves the rest of ExecStart. The old
// unit is kept as thefeed-server.service.bak in the data directory, as
// install.sh does. Then systemd reloads it and the server restarts.
func (m *mod) fixUnit(ctx context.Context) error {
	u, err := readUnit(m.Unit)
	uses, rest := shadowing(u.words)
	if err != nil || len(uses) == 0 {
		return &refused{http.StatusConflict, admin.FieldError{Message: "thefeed-server's unit passes no settings as flags."}}
	}
	const name = "Move the unit's flags into thefeed.env"
	envPath := filepath.Join(m.d.ServerDataDir, "thefeed.env")
	data, hash, err := serverconfig.ReadFile(envPath)
	if err != nil {
		return m.note(name, err)
	}
	env := serverconfig.ParseEnv(data)
	// /status has the value that won between flag and file, except secrets.
	var running map[string]string
	if st, err := m.status(ctx); err == nil {
		running = st.Server.Config
	}
	var moved []string
	for _, f := range uses {
		v, ok := running[f.Key]
		if !ok {
			v = f.value
		}
		if err := env.Set(f.Env, v); err != nil {
			return m.note(name, fmt.Errorf("Couldn't move --%s: %v", f.Flag, err))
		}
		moved = append(moved, "--"+f.Flag)
	}
	lines := slices.Concat(u.lines[:u.from], []string{"ExecStart=" + strings.Join(rest, " ")}, u.lines[u.to:])
	err = os.WriteFile(filepath.Join(m.d.ServerDataDir, "thefeed-server.service.bak"), u.data, 0o600)
	if err == nil {
		err = serverconfig.WriteFile(envPath, env.Bytes(), hash)
	}
	if err == nil {
		err = serverconfig.WriteFile(m.Unit, []byte(strings.Join(lines, "\n")), serverconfig.Hash(u.data))
	}
	if err == nil {
		_, err = m.Systemd.Run(ctx, "daemon-reload")
	}
	if err == nil {
		_, err = m.Systemd.Run(ctx, "restart", unit)
	}
	if err != nil {
		return m.note(name, err)
	}
	m.line("Moved "+strings.Join(moved, ", ")+" into thefeed.env and restarted", "")
	return m.note(name, nil)
}

// shadowed lists the flags in thefeed-server's unit that beat thefeed.env.
func (m *mod) shadowed() []string {
	u, _ := readUnit(m.Unit)
	uses, _ := shadowing(u.words)
	out := []string{}
	for _, f := range uses {
		out = append(out, "--"+f.Flag)
	}
	return out
}
