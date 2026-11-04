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
	"github.com/sartoopjj/thefeed/internal/version"
)

// modules is the app wiring: each feature module is one line here.
var modules = []admin.Module{}

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
	for name, env := range map[string]string{
		"listen": "THEFEED_ADMIN_LISTEN", "hostname": "THEFEED_ADMIN_HOSTNAME", "localhost": "THEFEED_ADMIN_LOCALHOST",
		"data-dir": "THEFEED_DATA_DIR", "admin-dir": "THEFEED_ADMIN_DIR",
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
			// ponytail: the running panel holds bbolt's lock; stopping it is
			// the operator's one-liner rather than an IPC channel to it.
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
