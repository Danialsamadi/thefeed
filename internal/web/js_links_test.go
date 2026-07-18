package web

import (
	"os/exec"
	"testing"
)

// TestJSLinkUnit runs the zero-dependency node unit tests for the frontend
// link-parsing helpers (parseTgLink, feedLinkTarget, findChannelByUsername).
// Skipped when node isn't installed.
func TestJSLinkUnit(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "jstest/links.test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("js unit tests failed:\n%s", out)
	}
	t.Logf("%s", out)
}
