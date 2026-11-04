package admin

import (
	"context"
	"os"
	"os/exec"

	"github.com/sartoopjj/thefeed/internal/serverconfig"
)

// InContainer is THEFEED_ADMIN_CONTAINER, which the panel's image sets.
func InContainer() bool {
	v, _ := serverconfig.ParseBool(os.Getenv("THEFEED_ADMIN_CONTAINER"))
	return v
}

// HostCommand runs name on the host. In the panel's container that's
// through nsenter into PID 1's namespaces, which pid: host and privileged
// allow.
func HostCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	if InContainer() {
		args = append([]string{"-t", "1", "-m", "-u", "-i", "-n", "-p", "--", name}, args...)
		name = "nsenter"
	}
	return exec.CommandContext(ctx, name, args...)
}
