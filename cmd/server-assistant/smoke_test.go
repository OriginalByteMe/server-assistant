//go:build smoke

// Smoke is the ADR 0008 boot contract: the sandbox proves a slice is alive by
// booting the real binary, waiting for the ready log line, sending SIGTERM, and
// asserting a clean exit. It is gated behind the `smoke` build tag so
// `go test ./...` (make test) never runs it — `make smoke` is the only entry.
// Pure stdlib (testing + os/exec); no new dependency (CONVENTIONS rule 1).
package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readyLine is the substring main.go logs once every seam is wired and the
// daemon is serving (slog JSON handler). Keep in sync with main.go's
// "server-assistant started" event.
const readyLine = `"msg":"server-assistant started"`

func TestSmokeBoot(t *testing.T) {
	tmp := t.TempDir()

	// Minimal config: schema v1, an ephemeral bind addr (127.0.0.1:0 → a free
	// port, so a parallel `make test` daemon cannot collide), a temp DB, no
	// services, no telegram (Stub notifier). Zero secrets, zero network.
	cfgPath := filepath.Join(tmp, "config.yaml")
	cfg := "schema_version: 1\n" +
		"http_addr: \"127.0.0.1:0\"\n" +
		"database:\n" +
		"  path: \"" + filepath.Join(tmp, "smoke.db") + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write smoke config: %v", err)
	}

	// Self-build the binary so the contract does not depend on `make build`
	// having run first. CGO_ENABLED=0 mirrors the real build (ADR 0004/0007).
	binPath := filepath.Join(tmp, "server-assistant")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binPath, "-config", cfgPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	// Wait for the ready line, with a hard deadline.
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), readyLine) {
				close(ready)
				return
			}
		}
	}()

	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("daemon never logged the ready line %q within 15s", readyLine)
	}

	// Graceful shutdown: SIGTERM must yield exit code 0 (main returns nil).
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon did not exit cleanly after SIGTERM: %v", err)
		}
	case <-time.After(12 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("daemon did not exit within 12s of SIGTERM")
	}
}
