// Command kitchensink runs the monitors in a config file and serves their
// snapshots and events to clients.
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
	_ "time/tzdata" // monitors take IANA zones regardless of the host's zone data

	"github.com/seventy-two/kitchensink/config"
	"github.com/seventy-two/kitchensink/engine"
	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/notify"
	"github.com/seventy-two/kitchensink/server"
	"github.com/seventy-two/kitchensink/store"

	// Each monitor kind registers itself; import a package to make its
	// kind available to the config file.
	_ "github.com/seventy-two/kitchensink/monitors/autotrader"
	_ "github.com/seventy-two/kitchensink/monitors/calendar"
	_ "github.com/seventy-two/kitchensink/monitors/nfl"
	_ "github.com/seventy-two/kitchensink/monitors/stocks"
	_ "github.com/seventy-two/kitchensink/monitors/todo"
	_ "github.com/seventy-two/kitchensink/monitors/weather"
)

// defaultInterval is used for monitors whose config doesn't set one.
const defaultInterval = 5 * time.Minute

func main() {
	path := flag.String("config", "kitchensink.json", "config file")
	check := flag.Bool("check", false, "check the config file and exit")
	flag.Parse()
	if err := run(*path, *check); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func run(path string, check bool) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	specs, err := buildMonitors(cfg)
	if err != nil {
		return err
	}
	notifiers, err := buildNotifiers(cfg)
	if err != nil {
		return err
	}
	if check {
		fmt.Printf("config OK: %d monitors, %d notifiers\n", len(specs), len(notifiers))
		return nil
	}

	st, err := store.Open(cfg.Database)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	log := slog.Default()
	eng, err := engine.New(st, specs, engine.Options{Log: log, Notifiers: notifiers})
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           (&server.Server{Engine: eng, Store: st, Token: cfg.Token, Log: log}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		eng.Run(ctx)
		close(done)
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()

	log.Info("listening", "addr", cfg.Listen, "monitors", len(specs))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done
	return nil
}

func buildMonitors(cfg *config.Config) ([]engine.Spec, error) {
	var specs []engine.Spec
	for _, mc := range cfg.Monitors {
		if mc.Disabled {
			continue
		}
		m, err := monitor.New(mc.Type, mc.Config)
		if err != nil {
			return nil, fmt.Errorf("monitor %s: %w", mc.ID, err)
		}
		interval := time.Duration(mc.Interval)
		if interval == 0 {
			interval = defaultInterval
		}
		specs = append(specs, engine.Spec{ID: mc.ID, Kind: mc.Type, Title: mc.Title, Interval: interval, Monitor: m})
	}
	return specs, nil
}

func buildNotifiers(cfg *config.Config) ([]engine.Notifier, error) {
	var out []engine.Notifier
	for i, nc := range cfg.Notifiers {
		switch nc.Type {
		case "ntfy":
			n, err := notify.NewNtfy(nc.Config)
			if err != nil {
				return nil, fmt.Errorf("notifier %d: %w", i, err)
			}
			out = append(out, n)
		default:
			return nil, fmt.Errorf("notifier %d: unknown type %q", i, nc.Type)
		}
	}
	return out, nil
}
