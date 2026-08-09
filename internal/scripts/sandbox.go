package scripts

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// bootGuardBindFailedExit and bootGuardROFailedExit are reserved inner-shell
// exit codes (distinct from any real script's own exit status, and from
// 124 which runWithTimeout already uses for a timeout) signalling that the
// protected-path bind mount or its read-only remount itself failed, as
// opposed to the script running and failing on its own.
const (
	bootGuardBindFailedExit = 97
	bootGuardROFailedExit   = 98
)

// shimsFS embeds the PATH shims ported from prototypes/dry-run/bin (see
// predicate.go's doc comment and FINDINGS.md). "all:" is required because
// _common.sh begins with an underscore, which go:embed otherwise skips.
//
//go:embed all:shims
var shimsFS embed.FS

// Executor is the production dry-run sandbox and the /boot-guarded real
// execution path. Every knob is config-driven (CONVENTIONS rule 6); zero
// values below are safe, conservative defaults.
type Executor struct {
	// DryRunTimeout bounds one dry-run attempt's wall clock. A dry run that
	// does not finish inside this window is rejection predicate branch (a)
	// (FINDINGS.md "runaway loop").
	DryRunTimeout time.Duration
	// RunTimeout bounds one real, post-approval execution's wall clock,
	// deliberately separate from DryRunTimeout (a real maintenance script
	// legitimately runs far longer than the bounded dry-run sandbox should
	// ever wait).
	RunTimeout time.Duration
	// ProtectedPaths are bind-mounted read-only for every real execution
	// (issue #51: never write /boot). Defaults to {"/boot"}.
	ProtectedPaths []string
	// DryRunScratch is the one directory a dry run's shimmed mutating calls
	// may target without tripping predicate branch (c). Left empty by
	// default — the same conservative ceiling FINDINGS.md found ("this
	// prototype's sandbox scope is always empty... every real mutating call
	// against a real production path trips this branch by design").
	DryRunScratch string
	// UnshareBin/StraceBin/BashBin are overridable for tests; production
	// wiring leaves them at their PATH-resolved defaults.
	UnshareBin string
	StraceBin  string
	BashBin    string

	capOnce sync.Once
	capErr  error
}

func (e *Executor) unshareBin() string {
	if e.UnshareBin != "" {
		return e.UnshareBin
	}
	return "unshare"
}
func (e *Executor) straceBin() string {
	if e.StraceBin != "" {
		return e.StraceBin
	}
	return "strace"
}
func (e *Executor) bashBin() string {
	if e.BashBin != "" {
		return e.BashBin
	}
	return "bash"
}
func (e *Executor) dryRunTimeout() time.Duration {
	if e.DryRunTimeout > 0 {
		return e.DryRunTimeout
	}
	return 15 * time.Second
}

// runTimeout bounds a real, post-approval execution — deliberately
// separate from dryRunTimeout: a real maintenance script legitimately runs
// longer than the bounded dry-run sandbox should ever wait.
func (e *Executor) runTimeout() time.Duration {
	if e.RunTimeout > 0 {
		return e.RunTimeout
	}
	return 5 * time.Minute
}
func (e *Executor) protectedPaths() []string {
	if len(e.ProtectedPaths) > 0 {
		return e.ProtectedPaths
	}
	return []string{"/boot"}
}

// probeCapability checks once whether this host can create the unprivileged
// user+mount namespace the sandbox depends on. A host without it gets the
// honest ceiling (ErrSandboxUnavailable), never a silent pass.
func (e *Executor) probeCapability(ctx context.Context) error {
	e.capOnce.Do(func() {
		cmd := exec.CommandContext(ctx, e.unshareBin(), "--user", "--map-root-user", "--mount", "--", "true")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			e.capErr = fmt.Errorf("%w: %s: %s", ErrSandboxUnavailable, e.unshareBin(), strings.TrimSpace(stderr.String()))
		}
	})
	return e.capErr
}

// scriptArgv is the ONLY place a script's own invocation argv is built.
// Scripts never receive arguments (issue #51) — extraArgs exists solely so
// this guard has something to reject; both DryRun and Run always call it
// with nil. Delete this check and TestScriptArgv_RejectsArguments fails.
func scriptArgv(bash, scriptPath string, extraArgs []string) ([]string, error) {
	if len(extraArgs) != 0 {
		return nil, fmt.Errorf("%w: %v", ErrArgumentsRejected, extraArgs)
	}
	return []string{bash, scriptPath}, nil
}

// extractShims writes the embedded shim scripts into dir with the exec bit
// set (go:embed does not preserve file permissions).
func extractShims(dir string) error {
	return fs.WalkDir(shimsFS, "shims", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := shimsFS.ReadFile(p)
		if err != nil {
			return err
		}
		name := filepath.Base(p)
		return os.WriteFile(filepath.Join(dir, name), data, 0o755)
	})
}

// DryRunResult is the evidence one dry-run attempt produced. Approved is
// APPROVED-FOR-REVIEW in FINDINGS.md's language — evidence, never proof.
type DryRunResult struct {
	Approved   bool
	ExitCode   int
	TimedOut   bool
	Reasons    []string
	Warnings   []string
	Transcript []TranscriptEntry
}

// DryRun runs body inside the shimmed, read-only, network-less sandbox
// (FINDINGS.md architecture) and applies the rejection predicate exactly as
// measured. It never returns a Go error for an ordinary rejection — a
// rejected dry run is a valid, useful result; error is reserved for
// something this call could not even attempt (context cancellation, I/O
// failure writing the script/shims).
func (e *Executor) DryRun(ctx context.Context, body string) (*DryRunResult, error) {
	if err := e.probeCapability(ctx); err != nil {
		return &DryRunResult{Approved: false, Reasons: []string{err.Error()}}, nil
	}

	work, err := os.MkdirTemp("", "sa-dryrun-*")
	if err != nil {
		return nil, fmt.Errorf("scripts: create dry-run workdir: %w", err)
	}
	defer os.RemoveAll(work)

	shimDir := filepath.Join(work, "bin")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		return nil, fmt.Errorf("scripts: create shim dir: %w", err)
	}
	if err := extractShims(shimDir); err != nil {
		return nil, fmt.Errorf("scripts: extract shims: %w", err)
	}

	scriptPath := filepath.Join(work, "script.sh")
	if err := os.WriteFile(scriptPath, []byte(body), 0o700); err != nil {
		return nil, fmt.Errorf("scripts: write script: %w", err)
	}

	argv, err := scriptArgv(e.bashBin(), scriptPath, nil)
	if err != nil {
		return nil, err
	}

	transcriptPath := filepath.Join(work, "transcript.log")
	stracePath := filepath.Join(work, "strace.log")
	transcriptFile, err := os.Create(transcriptPath)
	if err != nil {
		return nil, fmt.Errorf("scripts: create transcript: %w", err)
	}
	defer transcriptFile.Close()
	straceFile, err := os.Create(stracePath)
	if err != nil {
		return nil, fmt.Errorf("scripts: create strace log: %w", err)
	}
	defer straceFile.Close()

	scratch := e.DryRunScratch
	if scratch == "" {
		scratch = "/nonexistent-scratch"
	}

	inner := fmt.Sprintf(`set -uo pipefail
mount --bind / / 2>/dev/null
mount -o remount,bind,ro / 2>/dev/null
cd /
if command -v %s >/dev/null 2>&1; then
  exec %s -f -qq -e trace=execve -o /proc/self/fd/4 %s
else
  exec %s
fi
`, e.straceBin(), e.straceBin(), shellQuoteArgv(argv), shellQuoteArgv(argv))

	runCtx, cancel := context.WithTimeout(ctx, e.dryRunTimeout()+5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, e.unshareBin(),
		"--user", "--map-root-user", "--mount", "--net", "--fork", "--",
		e.bashBin(), "-c", "ip link set lo up >/dev/null 2>&1; "+inner)
	cmd.Env = append(os.Environ(),
		"PATH="+shimDir+":"+os.Getenv("PATH"),
		"DRYRUN_TRANSCRIPT_FD=3",
		"DRYRUN_SCRATCH="+scratch,
	)
	cmd.ExtraFiles = []*os.File{transcriptFile, straceFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	exitCode, timedOut := runWithTimeout(cmd, e.dryRunTimeout())

	approved, reasons, warnings, entries := verdict(transcriptPath, stracePath, shimDir, exitCode, timedOut)
	return &DryRunResult{
		Approved:   approved,
		ExitCode:   exitCode,
		TimedOut:   timedOut,
		Reasons:    reasons,
		Warnings:   warnings,
		Transcript: entries,
	}, nil
}

// RunResult is the outcome of a real, post-approval execution. Run returns
// a non-nil error instead of a RunResult when the /boot guard itself could
// not be established (fail closed, never run unguarded).
type RunResult struct {
	ExitCode  int
	Succeeded bool
	TimedOut  bool
	Output    string
}

// Run executes body for real, for keeps, with every path in
// e.protectedPaths() bind-mounted read-only first (issue #51: scripts may
// never write /boot, enforced by the executor). Unlike DryRun, PATH is the
// real PATH and there is no network namespace: the script's mutations are
// meant to happen. If the sandbox capability this guard depends on is
// unavailable, Run refuses to execute at all — the /boot ban is never
// downgraded to a convention.
func (e *Executor) Run(ctx context.Context, body string) (*RunResult, error) {
	if err := e.probeCapability(ctx); err != nil {
		return nil, err
	}

	work, err := os.MkdirTemp("", "sa-run-*")
	if err != nil {
		return nil, fmt.Errorf("scripts: create run workdir: %w", err)
	}
	defer os.RemoveAll(work)

	scriptPath := filepath.Join(work, "script.sh")
	if err := os.WriteFile(scriptPath, []byte(body), 0o700); err != nil {
		return nil, fmt.Errorf("scripts: write script: %w", err)
	}

	argv, err := scriptArgv(e.bashBin(), scriptPath, nil)
	if err != nil {
		return nil, err
	}

	var mounts strings.Builder
	for _, p := range e.protectedPaths() {
		bindMsg := shellQuote(fmt.Sprintf("scripts: bind mount failed for protected path %s", p))
		roMsg := shellQuote(fmt.Sprintf("scripts: read-only remount failed for protected path %s", p))
		fmt.Fprintf(&mounts, "mount --bind %s %s || { echo %s >&2; exit %d; }\n",
			shellQuote(p), shellQuote(p), bindMsg, bootGuardBindFailedExit)
		fmt.Fprintf(&mounts, "mount -o remount,bind,ro %s || { echo %s >&2; exit %d; }\n",
			shellQuote(p), roMsg, bootGuardROFailedExit)
	}

	inner := fmt.Sprintf(`set -uo pipefail
%s
exec %s
`, mounts.String(), shellQuoteArgv(argv))

	runCtx, cancel := context.WithTimeout(ctx, e.runTimeout()+5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, e.unshareBin(),
		"--user", "--map-root-user", "--mount", "--fork", "--",
		e.bashBin(), "-c", inner)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	exitCode, timedOut := runWithTimeout(cmd, e.runTimeout())

	// The /boot (or any configured protected-path) guard is load-bearing:
	// if the bind mount or the read-only remount itself failed, the script
	// never ran at all — fail closed with a distinct error rather than
	// reporting a false Succeeded/Failed RunResult that implies the guard
	// held when it never was established.
	if !timedOut && (exitCode == bootGuardBindFailedExit || exitCode == bootGuardROFailedExit) {
		return nil, fmt.Errorf("%w: %s", ErrProtectedPathGuardFailed, strings.TrimSpace(out.String()))
	}

	return &RunResult{
		ExitCode:  exitCode,
		Succeeded: exitCode == 0 && !timedOut,
		TimedOut:  timedOut,
		Output:    out.String(),
	}, nil
}

// runWithTimeout runs cmd, hard-killing its whole process group as a
// backstop if it outlives timeout (the inner shell also races its own
// `command -v strace` timeout for the dry-run case; this is the outer,
// unconditional guarantee for both paths).
func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) (exitCode int, timedOut bool) {
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return -1, false
	}
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			return 0, false
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), false
		}
		return -1, false
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		return 124, true
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shellQuoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}
