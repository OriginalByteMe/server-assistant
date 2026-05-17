// Command server-assistant is the composition root: it loads config, wires the
// seams, and runs until a shutdown signal. Issue 0001 wires stubs only.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"server-assistant/internal/config"
	"server-assistant/internal/core"
	"server-assistant/internal/notifier"
	"server-assistant/internal/prober"
	"server-assistant/internal/store"
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

	// Wire the remaining seams. Stubs for issue 0001; real implementations
	// attach behind these same seams in later issues (ADR 0006).
	var (
		p core.Prober   = prober.Stub{}
		n core.Notifier = notifier.Stub{}
	)
	_ = p
	_ = n

	slog.Info("server-assistant started",
		"schema_version", cfg.SchemaVersion,
		"http_addr", cfg.HTTPAddr,
		"db", cfg.Database.Path)

	<-ctx.Done()
	slog.Info("shutdown signal received, stopping")
	return nil
}
