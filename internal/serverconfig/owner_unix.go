//go:build unix

package serverconfig

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// chownLike gives name to whoever owns like, or like's directory while like
// doesn't exist yet. The panel runs as root, and thefeed-server may not:
// its container runs as uid 1000, and a root-owned 0600 file would lock it
// out of its own settings. Only root can give a file away; others skip it.
func chownLike(name, like string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	fi, err := os.Stat(like)
	if errors.Is(err, os.ErrNotExist) {
		fi, err = os.Stat(filepath.Dir(like))
	}
	if err != nil {
		return err
	}
	st := fi.Sys().(*syscall.Stat_t)
	return os.Chown(name, int(st.Uid), int(st.Gid))
}
