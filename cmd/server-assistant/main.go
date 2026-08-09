// Command server-assistant is the composition root: it loads config, wires the
// seams, runs the monitor spine, and serves the dashboard until a shutdown
// signal.
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
	"sync"
	"syscall"
	"time"

	"server-assistant/internal/actuator"
	"server-assistant/internal/config"
	"server-assistant/internal/core"
	"server-assistant/internal/harness"
	"server-assistant/internal/monitor"
	"server-assistant/internal/notifier"
	"server-assistant/internal/prober"
	"server-assistant/internal/reasoner"
	"server-assistant/internal/store"
	"server-assistant/internal/tools"
	"server-assistant/internal/web"
)

// telegramTimeout caps a single Alert delivery (CONVENTIONS rule 4). It is a
// fixed daemon constant, not config: a one-way Alert that cannot send within
// this budget is dropped (logged by the monitor) rather than stalling a poll.
const (
	telegramTimeout      = 10 * time.Second
	configReloadInterval = 2 * time.Second
	// The Harness self-probe (ADR 0015) is a cheap reachability check on the
	// Reasoner and the write credential, so it polls far slower than a
	// Service and tolerates a slow local model before it reads DEGRADED.
	harnessProbePoll      = 60 * time.Second
	harnessProbeThreshold = 30 * time.Second
)

// sshRunnerFor builds an SSH Runner from one credential block. Two exist by
// design (ADR 0022): the shared read-only probe key and the harness's scoped
// write key. Neither the key nor the password is ever logged (rule 8).
func sshRunnerFor(c config.SSHConfig) (prober.Runner, error) {
	var keyPEM []byte
	if c.KeyFile != "" {
		b, err := os.ReadFile(c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("read ssh key_file: %w", err)
		}
		keyPEM = b
	}
	hostKey, insecure, err := prober.ParseHostKey(c.HostKey)
	if err != nil {
		return nil, err
	}
	if insecure {
		slog.Warn("ssh host key not pinned — accepting any host key (ADR 0003 defers hardening to M2); set host_key to pin", "address", c.Address)
	}
	return prober.NewSSHClient(prober.SSHConfig{
		Address:         c.Address,
		User:            c.User,
		Password:        c.Password,
		PrivateKey:      keyPEM,
		Timeout:         c.ProbeTimeout(),
		HostKeyCallback: hostKey,
	}), nil
}

// harnessSecrets is everything this process holds that must never appear in a
// prompt. Scrubbing is fail-closed at the Reasoner seam (ADR 0013): if any of
// these survives into a payload, the Diagnosis is abandoned rather than sent.
func harnessSecrets(cfg *config.Config) []string {
	var out []string
	add := func(s string) {
		if len(s) >= 2 {
			out = append(out, s)
		}
	}
	if cfg.SSH != nil {
		add(cfg.SSH.Password)
	}
	if cfg.Harness != nil {
		add(cfg.Harness.Reasoner.APIKey)
		if cfg.Harness.WriteSSH != nil {
			add(cfg.Harness.WriteSSH.Password)
		}
	}
	add(cfg.Telegram.BotToken)
	return out
}

// dashboard wires the HTTP surface. With no Harness the v1 dashboard is served
// unchanged; with one, the incident/Approval routes attach behind the same
// handler (ADR 0023 — the dashboard is this milestone's Approval surface).
func dashboard(mon *monitor.Monitor, hs *harness.Harness) http.Handler {
	if hs == nil {
		return web.Handler(mon)
	}
	return web.HandlerWithHarness(mon, hs)
}

func reloadKnobs(src config.Source, ctx context.Context) ([]monitor.Service, error) {
	cfg, err := src.Load(ctx)
	if err != nil {
		return nil, err
	}
	svcs := make([]monitor.Service, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		svcs = append(svcs, monitor.Service{
			Name:      s.Name,
			Threshold: s.Threshold(),
			Poll:      s.Poll(),
			DebounceN: s.DebounceN,
		})
	}
	return svcs, nil
}

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

	// Shared SSH Runner for container-state / host-metrics probes (ARK-13).
	// Built once; absent ⇒ no SSH probes (config.validate guarantees no
	// container Service / ssh_metrics Host exists without this block).
	var sshRunner prober.Runner
	if cfg.SSH != nil {
		runner, rerr := sshRunnerFor(*cfg.SSH)
		if rerr != nil {
			return rerr
		}
		sshRunner = runner
		slog.Info("ssh probes enabled", "host", cfg.SSH.Address, "user", cfg.SSH.User) // never the secret (rule 8)
	}

	svcs := make([]monitor.Service, 0, len(cfg.Services))
	for _, s := range cfg.Services {
		// Probe kind is unambiguous: config.validate() guarantees exactly one
		// of url / tcp / container. All feed the same prober-agnostic spine.
		var p core.Prober
		switch {
		case s.Container != "":
			p = prober.NewContainerProbe(s.Name, sshRunner, s.Container)
		case s.TCPAddr != "":
			p = prober.NewTCP(s.Name, s.TCPAddr, s.ProbeTimeout())
		default:
			p = prober.NewHTTP(s.Name, s.URL, s.ProbeTimeout())
		}
		svcs = append(svcs, monitor.Service{
			Name:      s.Name,
			Prober:    p,
			Threshold: s.Threshold(),
			Poll:      s.Poll(),
			DebounceN: s.DebounceN,
		})
	}

	// M2 Harness (ADR 0009). Default-off (ADR 0014): with no harness block,
	// or mode off, nothing here changes the v1 spine's behaviour.
	var hs *harness.Harness
	if mode, merr := core.ParseHarnessMode(cfg.Harness.Mode); merr != nil {
		return merr
	} else if mode != core.HarnessOff {
		if sshRunner == nil {
			return errors.New("harness enabled but no ssh: block — the read-only Diagnosis tools need it")
		}
		// Scoped write credential (ADR 0022): a leaked read key must never be
		// able to actuate. Absent in shadow mode, where nothing is dispatched.
		var writeRunner prober.Runner
		if cfg.Harness.WriteSSH != nil {
			runner, werr := sshRunnerFor(*cfg.Harness.WriteSSH)
			if werr != nil {
				return werr
			}
			writeRunner = runner
			slog.Info("harness write credential wired", "host", cfg.Harness.WriteSSH.Address) // never the key (rule 8)
		}
		var act core.Actuator
		if writeRunner != nil {
			act = actuator.NewSSH(writeRunner, cfg.Harness.AllowRestart)
		}
		hs = harness.New(harness.Options{
			Mode:  mode,
			Store: st,
			Reasoner: reasoner.New(reasoner.Config{
				BaseURL: cfg.Harness.Reasoner.BaseURL,
				Model:   cfg.Harness.Reasoner.Model,
				APIKey:  cfg.Harness.Reasoner.APIKey,
				Timeout: cfg.Harness.Reasoner.Timeout,
				// Everything the process holds that must never reach a
				// prompt (ADR 0013, fail-closed).
				Secrets: harnessSecrets(cfg),
			}),
			Actuator: act,
			Tools: []core.ReadTool{
				tools.ContainerStatus(sshRunner, cfg.Harness.Targets),
				tools.ContainerLogs(sshRunner, cfg.Harness.Targets, cfg.Harness.LogLines),
				tools.StatusHistory(st, cfg.Harness.Ceilings.MaxToolCalls*10),
			},
			Targets:         cfg.Harness.Targets,
			MaxToolCalls:    cfg.Harness.Ceilings.MaxToolCalls,
			WallClock:       cfg.Harness.Ceilings.WallClock,
			ApprovalTimeout: cfg.Harness.ApprovalTimeout,
			Cooldown:        cfg.Harness.Cooldown,
			OutcomeWindow:   cfg.Harness.OutcomeWindow,
		})
		// The Harness is itself a monitored subject on the v1 spine (ADR
		// 0015): its dependencies are probed, debounced, and Alerted exactly
		// like any Service. Added programmatically, not from config — a
		// hot-reload leaves it alone (Monitor.Reconfigure skips unknown
		// subjects).
		svcs = append(svcs, monitor.Service{
			Name:      "harness",
			Prober:    hs.Prober(),
			Threshold: harnessProbeThreshold,
			Poll:      harnessProbePoll,
			DebounceN: 2,
		})
		// Resolve any cycle whose owning goroutine died with a previous
		// process. Must run before the Monitor can feed ObserveCommit, or a
		// restart leaves an Approval pending forever with nobody to time it
		// out (ADR 0019: the audit record has to stay truthful).
		if rerr := hs.Reconcile(ctx); rerr != nil {
			return fmt.Errorf("harness reconcile: %w", rerr)
		}
		slog.Info("harness enabled", "mode", mode.String(), "targets", len(cfg.Harness.Targets))
	}

	mon := monitor.New(st, notify, svcs)
	// The Harness observes committed Status after every v1 outcome has landed
	// (persist + Alert + dashboard). Sink errors cannot change a monitoring
	// result.
	if hs != nil {
		mon.SetCommitSink(hs.Sink())
	}
	// Rolling Probe-sample retention (ARK-9 / ADR 0002): bound history so
	// storage cannot grow unbounded. Always set (config defaults to 24h).
	mon.SetRetention(cfg.History.Window())
	// Optional Host reachability gate (ADR 0005). Absent => no gate, the bare
	// spine is wired unchanged (ADR 0006 rule 2). SetHost must precede Resume
	// so a restart restores the gate from the persisted Host Status.
	if cfg.Host != nil {
		// ssh_metrics drives Host Status from array/disk/parity + CPU/RAM
		// over SSH; otherwise bare TCP reachability (ARK-12). Either way the
		// Prober's UP/DEGRADED/DOWN feeds the same gate (ADR 0005).
		var hostProber core.Prober
		if cfg.Host.SSHMetrics {
			hostProber = prober.NewHostMetricsProbe(cfg.Host.Name, sshRunner)
			slog.Info("host gate via ssh metrics enabled", "host", cfg.Host.Name)
		} else {
			hostProber = prober.NewReachability(cfg.Host.Name, cfg.Host.Address, cfg.Host.ProbeTimeout())
			slog.Info("host reachability gate enabled", "host", cfg.Host.Name)
		}
		mon.SetHost(monitor.Host{
			Name:      cfg.Host.Name,
			Prober:    hostProber,
			Poll:      cfg.Host.Poll(),
			DebounceN: cfg.Host.DebounceN,
		})
	}
	if err := mon.Resume(ctx); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           dashboard(mon, hs),
		ReadHeaderTimeout: 5 * time.Second,
	}

	var lastConfigMod time.Time
	if info, statErr := os.Stat(*cfgPath); statErr != nil {
		slog.Error("stat config for reload watcher", "err", statErr)
	} else {
		lastConfigMod = info.ModTime()
	}

	var wg sync.WaitGroup
	wg.Add(3)
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
	go func() {
		defer wg.Done()
		t := time.NewTicker(configReloadInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				info, statErr := os.Stat(*cfgPath)
				if statErr != nil {
					slog.Error("stat config for reload watcher", "err", statErr)
					continue
				}
				mod := info.ModTime()
				if mod.Equal(lastConfigMod) {
					continue
				}
				knobs, loadErr := reloadKnobs(config.NewFileSource(*cfgPath), ctx)
				if loadErr != nil {
					lastConfigMod = mod
					slog.Error("config reload rejected, keeping previous config", "err", loadErr)
					continue
				}
				mon.Reconfigure(knobs)
				lastConfigMod = mod
				slog.Info("config reloaded", "services", len(knobs))
			}
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
