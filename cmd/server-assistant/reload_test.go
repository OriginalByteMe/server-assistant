package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"server-assistant/internal/config"
)

func TestReloadKnobsLoadsValidConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	cfg := "schema_version: 1\n" +
		"http_addr: \"127.0.0.1:0\"\n" +
		"database:\n" +
		"  path: \"" + filepath.Join(tmp, "server-assistant.db") + "\"\n" +
		"services:\n" +
		"  - name: web\n" +
		"    url: https://example.com\n" +
		"    poll_interval: 7s\n" +
		"    timeout: 3s\n" +
		"    latency_threshold: 250ms\n" +
		"    debounce_n: 2\n" +
		"  - name: tcp\n" +
		"    tcp: 127.0.0.1:80\n" +
		"    poll_interval: 11s\n" +
		"    latency_threshold: 1s\n" +
		"    debounce_n: 4\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got, err := reloadKnobs(config.NewFileSource(cfgPath), context.Background())
	if err != nil {
		t.Fatalf("reloadKnobs valid config: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("reloadKnobs services len = %d, want 2", len(got))
	}
	want := []struct {
		name      string
		threshold time.Duration
		poll      time.Duration
		debounceN int
	}{
		{name: "web", threshold: 250 * time.Millisecond, poll: 7 * time.Second, debounceN: 2},
		{name: "tcp", threshold: time.Second, poll: 11 * time.Second, debounceN: 4},
	}
	for i := range want {
		if got[i].Name != want[i].name {
			t.Fatalf("service %d name = %q, want %q", i, got[i].Name, want[i].name)
		}
		if got[i].Threshold != want[i].threshold {
			t.Fatalf("service %s threshold = %s, want %s", got[i].Name, got[i].Threshold, want[i].threshold)
		}
		if got[i].Poll != want[i].poll {
			t.Fatalf("service %s poll = %s, want %s", got[i].Name, got[i].Poll, want[i].poll)
		}
		if got[i].DebounceN != want[i].debounceN {
			t.Fatalf("service %s debounce_n = %d, want %d", got[i].Name, got[i].DebounceN, want[i].debounceN)
		}
		if got[i].Prober != nil {
			t.Fatalf("service %s prober = %T, want nil", got[i].Name, got[i].Prober)
		}
	}
}

func TestReloadKnobsRejectsInvalidConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	cfg := "schema_version: 1\n" +
		"database:\n" +
		"  path: \"" + filepath.Join(tmp, "server-assistant.db") + "\"\n" +
		"services:\n" +
		"  - name: web\n" +
		"    url: https://example.com\n" +
		"    poll_interval: not-a-duration\n" +
		"    latency_threshold: 250ms\n" +
		"    debounce_n: 2\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := reloadKnobs(config.NewFileSource(cfgPath), context.Background()); err == nil {
		t.Fatal("reloadKnobs invalid config returned nil error")
	}
}
