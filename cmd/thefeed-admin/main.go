// Command thefeed-admin is the web panel that runs beside thefeed-server.
//
//	thefeed-admin [serve] [flags]   serve the panel (the default)
//	thefeed-admin reset-admin       delete the admin and sessions, print a new setup code
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sartoopjj/thefeed/internal/admin"
	"github.com/sartoopjj/thefeed/internal/admin/health"
	"github.com/sartoopjj/thefeed/internal/admin/logs"
	"github.com/sartoopjj/thefeed/internal/admin/service"
	"github.com/sartoopjj/thefeed/internal/admin/settings"
	"github.com/sartoopjj/thefeed/internal/admin/sysmetrics"
	"github.com/sartoopjj/thefeed/internal/admin/web"
	"github.com/sartoopjj/thefeed/internal/serverconfig"
	"github.com/sartoopjj/thefeed/internal/version"
)

// modules is the app wiring: each feature module is one line here.
var modules = []admin.Module{
	health.Module,
	logs.Module,
	service.Module,
	settings.Module,
	sysmetrics.Module,
	web.Module,
}

func main() {
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("thefeed-admin", flag.ExitOnError)
	listen := fs.String("listen", ":443", "HTTPS listen address (env THEFEED_ADMIN_LISTEN)")
	hostname := fs.String("hostname", "", "Panel hostname for a Let's Encrypt certificate; empty = self-signed (env THEFEED_ADMIN_HOSTNAME)")
	localhost := fs.Bool("localhost", false, "Bind 127.0.0.1 with plain HTTP, for an SSH tunnel (env THEFEED_ADMIN_LOCALHOST)")
	dataDir := fs.String("data-dir", "/opt/thefeed/data", "thefeed-server's data directory (env THEFEED_DATA_DIR)")
	adminDir := fs.String("admin-dir", "", "The panel's own data directory (default: admin next to --data-dir; env THEFEED_ADMIN_DIR)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "thefeed-admin %s\n\nThe web panel for thefeed-server.\n\nUsage:\n"+
			"  thefeed-admin [serve] [flags]   serve the panel (default)\n"+
			"  thefeed-admin reset-admin [flags]   delete the admin account and sessions, print a new setup code\n\nFlags:\n", version.Version)
		fs.PrintDefaults()
	}
	fs.Parse(args)

	// Env fills in any flag not typed on the command line; typed flags win.
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	// The data directory first: the panel's own settings are in its
	// thefeed.env too, as the server's are, so a container needs no env
	// file. The real environment wins, and the file can't move the directory.
	if v := os.Getenv("THEFEED_DATA_DIR"); v != "" && !given["data-dir"] {
		*dataDir = v
	}
	envData, _, err := serverconfig.ReadFile(filepath.Join(*dataDir, "thefeed.env"))
	if err != nil {
		log.Printf("thefeed-admin: %v", err)
	}
	serverconfig.ParseEnv(envData).Setenv()
	for name, env := range map[string]string{
		"listen": "THEFEED_ADMIN_LISTEN", "hostname": "THEFEED_ADMIN_HOSTNAME", "localhost": "THEFEED_ADMIN_LOCALHOST",
		"admin-dir": "THEFEED_ADMIN_DIR",
	} {
		if v := os.Getenv(env); v != "" && !given[name] {
			if err := fs.Set(name, v); err != nil {
				log.Fatalf("%s=%q: %v", env, v, err)
			}
		}
	}
	if *adminDir == "" {
		*adminDir = filepath.Join(filepath.Dir(filepath.Clean(*dataDir)), "admin")
	}
	cfg := admin.Config{AdminDir: *adminDir, ServerDataDir: *dataDir, Listen: *listen, Hostname: *hostname, LocalhostOnly: *localhost}

	switch cmd {
	case "serve":
		app, err := admin.New(cfg, modules...)
		if err != nil {
			log.Fatalf("thefeed-admin: %v", err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		err = app.Run(ctx)
		stop()
		app.Close()
		if err != nil {
			log.Fatalf("thefeed-admin: %v", err)
		}
	case "reset-admin":
		code, err := admin.ResetAdmin(cfg)
		if errors.Is(err, admin.ErrBusy) {
			// The running panel holds bbolt's lock; stopping it is
			// the operator's one-liner rather than an IPC channel to it.
			if admin.InContainer() {
				log.Fatalf("thefeed-admin is running. Run this on the host instead:\n" +
					"  docker stop thefeed-admin\n" +
					"  docker run --rm -v /opt/thefeed:/opt/thefeed $(docker inspect -f '{{.Config.Image}}' thefeed-admin) reset-admin\n" +
					"  docker start thefeed-admin")
			}
			log.Fatalf("thefeed-admin is running. Run this instead:\n  systemctl stop thefeed-admin && thefeed-admin reset-admin && systemctl start thefeed-admin")
		}
		if err != nil {
			log.Fatalf("reset-admin: %v", err)
		}
		fmt.Printf("The admin account and every session are gone.\nSetup code: %s (works once, for 24 hours)\nOpen the panel and create the admin account with it.\n", code)
	default:
		fs.Usage()
		os.Exit(2)
	}
}
