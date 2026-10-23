// Command vps-dashboard is a read-only dashboard for a small shared server,
// organised by project. One binary, two roles:
//
//	vps-dashboard serve  # web UI + JSON API on 127.0.0.1, no privileges
//	vps-dashboard probe  # reads env names, backups, logs; no network
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/olayanju-1234/vps-dashboard/internal/collect"
	"github.com/olayanju-1234/vps-dashboard/internal/config"
	"github.com/olayanju-1234/vps-dashboard/internal/demo"
	"github.com/olayanju-1234/vps-dashboard/internal/probe"
	"github.com/olayanju-1234/vps-dashboard/internal/server"
	"github.com/olayanju-1234/vps-dashboard/web"
)

// version is set at build time with -ldflags "-X main.version=<sha>".
var version = "dev"

const defaultConfig = "/etc/vps-dashboard/config.json"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "probe":
		err = runProbe(args)
	case "demo":
		err = runDemo(args)
	case "check-config":
		err = checkConfig(args)
	case "version", "--version", "-v":
		fmt.Println(version)
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vps-dashboard:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: vps-dashboard <command> [flags]

commands:
  serve         run the web UI and JSON API (unprivileged, loopback only)
  probe         run the privileged reader that writes the snapshot (no network)
  demo          serve made-up data, for trying the UI anywhere
  check-config  validate a config file
  version       print the version
`)
}

func logger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfig, "config file")
	level := fs.String("log-level", "info", "debug, info, warn or error")
	_ = fs.Parse(args)
	log := logger(*level)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	col := collect.New(cfg, version, log)
	go col.Run(ctx)
	log.Info("serve: listening", "addr", cfg.Listen, "version", version, "projects", len(cfg.Projects))
	return listen(ctx, cfg.Listen, server.New(col, web.FS(), version, log), log)
}

func listen(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: shutdown: %w", err)
	}
	log.Info("serve: stopped")
	return nil
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfig, "config file")
	level := fs.String("log-level", "info", "debug, info, warn or error")
	once := fs.Bool("once", false, "write one snapshot and exit")
	_ = fs.Parse(args)
	log := logger(*level)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	p := probe.New(cfg, version, log)
	if *once {
		snap := p.Cycle(ctx)
		if err := probe.Write(cfg.SnapshotPath, snap); err != nil {
			return err
		}
		log.Info("probe: wrote snapshot", "path", cfg.SnapshotPath, "env_files", len(snap.EnvFiles), "backups", len(snap.Backups), "errors", len(snap.Errors))
		return nil
	}
	log.Info("probe: running", "every", cfg.Intervals.Probe.String(), "snapshot", cfg.SnapshotPath, "version", version)
	return p.Run(ctx)
}

func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	addr := fs.String("listen", "127.0.0.1:8400", "address to listen on")
	_ = fs.Parse(args)
	log := logger("info")
	ctx, stop := signalContext()
	defer stop()
	log.Info("demo: made-up data", "url", "http://"+*addr)
	return listen(ctx, *addr, server.New(demo.New(version), web.FS(), version, log), log)
}

func checkConfig(args []string) error {
	fs := flag.NewFlagSet("check-config", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfig, "config file")
	_ = fs.Parse(args)
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Printf("ok: %d projects, listen %s, snapshot %s\n", len(cfg.Projects), cfg.Listen, cfg.SnapshotPath)
	for _, p := range cfg.Projects {
		fmt.Printf("  %-18s units=%d ports=%d checks=%d env_files=%d backups=%d deploy=%s\n",
			p.ID, len(p.Units), len(p.Ports), len(p.Checks), len(p.EnvFiles), len(p.Backups), p.Deploy.Method)
	}
	return nil
}
