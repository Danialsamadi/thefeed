package web

import (
	"os/exec"
	"testing"
)

// TestJSUtil runs the zero-dependency node tests for the UI's pure helpers.
// Skipped when node isn't installed.
func TestJSUtil(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "jstest/util.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("js unit tests failed:\n%s", out)
	}
	t.Logf("%s", out)
}
