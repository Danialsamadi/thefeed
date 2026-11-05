//go:build unix

package serverconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func fileUID(fi os.FileInfo) uint32 { return fi.Sys().(*syscall.Stat_t).Uid }
func fileGID(fi os.FileInfo) uint32 { return fi.Sys().(*syscall.Stat_t).Gid }

// TestWriteFileKeepsOwner needs root to give files away, so it skips
// elsewhere; CI runs it as root (.github/workflows/e2e.yml).
func TestWriteFileKeepsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to set a file's owner")
	}
	owner := func(t *testing.T, path string, uid, gid int) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%d:%d", fileUID(fi), fileGID(fi)); got != fmt.Sprintf("%d:%d", uid, gid) {
			t.Errorf("%s is owned by %s, want %d:%d", filepath.Base(path), got, uid, gid)
		}
	}

	// The server container runs as uid 1000 and wrote its own thefeed.env;
	// the panel, as root, rewrites it.
	t.Run("an existing file keeps its owner and mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "thefeed.env")
		if err := os.WriteFile(path, []byte("A=1\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, 1000, 1001); err != nil {
			t.Fatal(err)
		}
		if err := WriteFile(path, []byte("A=2\n"), Hash([]byte("A=1\n"))); err != nil {
			t.Fatal(err)
		}
		owner(t, path, 1000, 1001)
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
			t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
		}
	})

	// A list the panel creates in the server's data directory belongs to
	// whoever owns the directory.
	t.Run("a new file belongs to the directory's owner", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chown(dir, 1000, 1001); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "x_accounts.txt")
		if err := WriteFile(path, []byte("jack\n"), Hash(nil)); err != nil {
			t.Fatal(err)
		}
		owner(t, path, 1000, 1001)
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
		}
	})

	t.Run("a new server key belongs to the directory's owner", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chown(dir, 1000, 1001); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreateServerKey(dir); err != nil {
			t.Fatal(err)
		}
		owner(t, filepath.Join(dir, ServerKeyFile), 1000, 1001)
	})
}
