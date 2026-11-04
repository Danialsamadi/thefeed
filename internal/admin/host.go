package admin

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/sartoopjj/thefeed/internal/admin/dockerapi"
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

// ServerContainer is how thefeed-server runs, from THEFEED_ADMIN_SERVER:
// with docker, the Docker Engine on /var/run/docker.sock and the server's
// container, THEFEED_ADMIN_SERVER_CONTAINER (default thefeed-server); with
// systemd, the default, a nil client.
func ServerContainer() (*dockerapi.Client, string, error) {
	switch v := os.Getenv("THEFEED_ADMIN_SERVER"); v {
	case "", "systemd":
		return nil, "", nil
	case "docker":
		return dockerapi.New("/var/run/docker.sock"), cmp.Or(os.Getenv("THEFEED_ADMIN_SERVER_CONTAINER"), "thefeed-server"), nil
	default:
		return nil, "", fmt.Errorf("THEFEED_ADMIN_SERVER is %q; want systemd or docker", v)
	}
}
