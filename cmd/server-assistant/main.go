// Command server-assistant is the composition root: it loads config, wires the
// seams, runs the monitor spine, and serves the dashboard until a shutdown
// signal.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"server-assistant/internal/config"
	"server-assistant/internal/core"
	"server-assistant/internal/monitor"
	"server-assistant/internal/notifier"
	"server-assistant/internal/prober"
	"server-assistant/internal/store"
	"server-assistant/internal/web"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "config.yaml", "path to the YAML config file")
	flag.Parse()

	// Cancel the root context on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.NewFileSource(*cfgPath).Load(ctx)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, cfg.Database.Path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			slog.Error("closing store", "err", cerr)
		}
	}()
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	// Telegram Notifier is ARK-7; the stub keeps the seam wired meanwhile.
	var notify core.Notifier = notifier.Stub{}

	svcs := make([]monitor.Service, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		svcs = append(svcs, monitor.Service{
			Name:      s.Name,
			Prober:    prober.NewHTTP(s.Name, s.URL, s.ProbeTimeout()),
			Threshold: s.Threshold(),
			Poll:      s.Poll(),
			DebounceN: s.DebounceN,
		})
	}

	mon := monitor.New(st, notify, svcs)
	if err := mon.Resume(ctx); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           web.Handler(mon),
		ReadHeaderTimeout: 5 * time.Second,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		mon.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		if serr := srv.ListenAndServe(); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			slog.Error("http server", "err", serr)
			stop() // a dead dashboard should bring the daemon down cleanly
		}
	}()

	slog.Info("server-assistant started",
		"schema_version", cfg.SchemaVersion,
		"http_addr", cfg.HTTPAddr,
		"db", cfg.Database.Path,
		"services", len(svcs))

	<-ctx.Done()
	slog.Info("shutdown signal received, stopping")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if serr := srv.Shutdown(shutdownCtx); serr != nil {
		slog.Error("http shutdown", "err", serr)
	}
	wg.Wait()
	return nil
}
