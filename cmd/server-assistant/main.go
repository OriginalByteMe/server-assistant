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

// telegramTimeout caps a single Alert delivery (CONVENTIONS rule 4). It is a
// fixed daemon constant, not config: a one-way Alert that cannot send within
// this budget is dropped (logged by the monitor) rather than stalling a poll.
const telegramTimeout = 10 * time.Second

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

	// One-way Alert channel (ARK-7). Telegram when the Operator supplied
	// credentials; otherwise the Stub keeps the seam wired and logs Alerts.
	// Neither the token nor the chat id is ever logged (CONVENTIONS rule 8).
	var notify core.Notifier = notifier.Stub{}
	if cfg.Telegram.Configured() {
		tg, terr := notifier.NewTelegram(cfg.Telegram.BotToken, cfg.Telegram.ChatID, telegramTimeout)
		if terr != nil {
			return terr
		}
		notify = tg
		slog.Info("notifier: telegram enabled")
	} else {
		slog.Info("notifier: telegram not configured, using stub (alerts logged only)")
	}

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
	// Optional Host reachability gate (ADR 0005). Absent => no gate, the bare
	// spine is wired unchanged (ADR 0006 rule 2). SetHost must precede Resume
	// so a restart restores the gate from the persisted Host Status.
	if cfg.Host != nil {
		mon.SetHost(monitor.Host{
			Name:      cfg.Host.Name,
			Prober:    prober.NewReachability(cfg.Host.Name, cfg.Host.Address, cfg.Host.ProbeTimeout()),
			Poll:      cfg.Host.Poll(),
			DebounceN: cfg.Host.DebounceN,
		})
		slog.Info("host reachability gate enabled", "host", cfg.Host.Name)
	}
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
