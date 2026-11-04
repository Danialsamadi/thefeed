package web

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// TestJSUtil runs the zero-dependency node tests for the UI's pure helpers,
// every jstest/*.test.mjs. Skipped when node isn't installed.
func TestJSUtil(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	files, err := filepath.Glob("jstest/*.test.mjs")
	if err != nil || len(files) == 0 {
		t.Fatalf("no js tests found: %v", err)
	}
	for _, f := range files {
		out, err := exec.Command(node, f).CombinedOutput()
		if err != nil {
			t.Fatalf("%s failed:\n%s", f, out)
		}
		t.Logf("%s: %s", f, out)
	}
}
