package admin

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSelfSignedCertIsMadeOnceAndKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	_, fp1, err := selfSignedCert(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, fp2, err := selfSignedCert(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 || len(fp1) != 95 {
		t.Fatalf("fingerprints %q then %q: want one stable AB:CD:... SHA-256", fp1, fp2)
	}
	if fi, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key.pem: %v %v, want mode 0600", fi.Mode(), err)
	}
}
