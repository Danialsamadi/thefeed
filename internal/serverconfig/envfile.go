package serverconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrChangedElsewhere means the file on disk is no longer the one the edit
// was based on. Reload and try again.
var ErrChangedElsewhere = errors.New("changed elsewhere, reload")

// ErrUnsafeValue means the value can't be written to thefeed.env without
// quoting. systemd, bash's `source` and the server each read that file, and
// they disagree on quoting, so such values are refused, not quoted.
var ErrUnsafeValue = errors.New("the value has a space, quote or other character thefeed.env can't hold")

// Env is thefeed.env as its lines, so writing it back changes only the
// lines that were set: comments, blank lines, order, unknown keys, CRLF and
// a missing final newline all survive.
type Env struct{ lines []string }

func ParseEnv(data []byte) *Env { return &Env{strings.Split(string(data), "\n")} }

func (e *Env) Bytes() []byte { return []byte(strings.Join(e.lines, "\n")) }

// envLine reads a line the way the server's loader does. ok is false for
// blank lines and comments.
func envLine(line string) (key, value string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	key, value, ok = strings.Cut(strings.TrimPrefix(t, "export "), "=")
	return strings.TrimSpace(key), unquote(strings.TrimSpace(value)), ok
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// Get returns the value of key. If the key is there more than once, the
// last one counts, as it does for systemd's EnvironmentFile.
func (e *Env) Get(key string) (value string, ok bool) {
	for _, line := range e.lines {
		if k, v, isKV := envLine(line); isKV && k == key {
			value, ok = v, true
		}
	}
	return value, ok
}

// Set changes every line that sets key, keeping an "export " prefix and
// quotes, or appends KEY=value at the end. Every reader of the file then
// sees the same value.
func (e *Env) Set(key, value string) error {
	if strings.ContainsAny(value, " \t\r\n\"'`$\\") {
		return ErrUnsafeValue
	}
	found := false
	for i, line := range e.lines {
		k, _, isKV := envLine(line)
		if !isKV || k != key {
			continue
		}
		found = true
		eq := strings.Index(line, "=")
		old := strings.TrimRight(line[eq+1:], "\r")
		q := ""
		if t := strings.TrimSpace(old); len(t) >= 2 && unquote(t) != t {
			q = t[:1]
		}
		e.lines[i] = line[:eq+1] + q + value + q + line[eq+1+len(old):]
	}
	if !found {
		if n := len(e.lines); e.lines[n-1] == "" {
			e.lines = e.lines[:n-1]
		}
		e.lines = append(e.lines, key+"="+value, "")
	}
	return nil
}

// Hash identifies a file's content for WriteFile.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadFile returns a file and its hash. A missing file reads as empty, so
// its hash is Hash(nil) and WriteFile can create it.
func ReadFile(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return data, Hash(data), err
}

// One lock for every file; per-path locks if writes ever contend.
var writeMu sync.Mutex

// WriteFile replaces path with data, if the file still has the hash the
// edit was based on; otherwise it returns ErrChangedElsewhere. The write is
// atomic (temporary file, fsync, rename) and keeps the file's mode and
// owner; a new file gets 0600 and its directory's owner.
func WriteFile(path string, data []byte, wantHash string) error {
	writeMu.Lock()
	defer writeMu.Unlock()

	_, have, err := ReadFile(path)
	if err != nil {
		return err
	}
	if have != wantHash {
		return ErrChangedElsewhere
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(mode)
	}
	if err == nil {
		err = chownLike(tmp.Name(), path)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
