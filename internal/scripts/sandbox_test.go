package scripts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScriptArgv_RejectsArguments(t *testing.T) {
	// This is the no-arguments guard (issue #51: scripts never receive
	// arguments). Deleting the len(extraArgs) != 0 check inside scriptArgv
	// makes this test fail because err would be nil instead of
	// ErrArgumentsRejected.
	_, err := scriptArgv("bash", "/tmp/x.sh", []string{"--evil"})
	require.ErrorIs(t, err, ErrArgumentsRejected)

	argv, err := scriptArgv("bash", "/tmp/x.sh", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"bash", "/tmp/x.sh"}, argv)
}

func TestExecutor_DryRun_ApprovesCleanScript(t *testing.T) {
	e := &Executor{DryRunTimeout: 10 * time.Second}
	res, err := e.DryRun(context.Background(), "#!/bin/bash\necho hello\nexit 0\n")
	require.NoError(t, err)
	if res.Warnings != nil {
		t.Logf("warnings: %v", res.Warnings)
	}
	require.True(t, res.Approved, "reasons: %v", res.Reasons)
	require.Equal(t, 0, res.ExitCode)
}

func TestExecutor_DryRun_RejectsNonzeroExit(t *testing.T) {
	e := &Executor{DryRunTimeout: 10 * time.Second}
	res, err := e.DryRun(context.Background(), "#!/bin/bash\nexit 3\n")
	require.NoError(t, err)
	require.False(t, res.Approved)
	require.Contains(t, strings.Join(res.Reasons, "|"), "(a) script exited nonzero: 3")
}

func TestExecutor_DryRun_RejectsDidNotTerminate(t *testing.T) {
	e := &Executor{DryRunTimeout: 2 * time.Second}
	res, err := e.DryRun(context.Background(), "#!/bin/bash\nwhile true; do :; done\n")
	require.NoError(t, err)
	require.False(t, res.Approved)
	require.True(t, res.TimedOut)
	require.Contains(t, strings.Join(res.Reasons, "|"), "(a) did not terminate")
}

func TestExecutor_DryRun_RejectsBootWrite(t *testing.T) {
	// Predicate branch (c): the shim's own /boot check trips regardless of
	// whether /boot really exists on this machine (realpath -m is purely
	// textual). This is the dry-run half of the /boot ban.
	e := &Executor{DryRunTimeout: 10 * time.Second}
	res, err := e.DryRun(context.Background(), "#!/bin/bash\nrm -f /boot/config/authorized_keys\n")
	require.NoError(t, err)
	require.False(t, res.Approved)
	joined := strings.Join(res.Reasons, "|")
	require.Contains(t, joined, "(c)")
	require.Contains(t, joined, "boot-write-forbidden")
}

func TestExecutor_DryRun_RejectsMutatingCallOutsideScratch(t *testing.T) {
	// DryRunScratch defaults to empty/nonexistent (the same conservative
	// ceiling FINDINGS.md documents): any real mutating shim call trips (c).
	e := &Executor{DryRunTimeout: 10 * time.Second}
	res, err := e.DryRun(context.Background(), "#!/bin/bash\ntouch /tmp/should-not-matter\nrm -f /tmp/should-not-matter\n")
	require.NoError(t, err)
	require.False(t, res.Approved)
	require.Contains(t, strings.Join(res.Reasons, "|"), "outside-sandbox-scope")
}

func TestExecutor_DryRun_ApprovesWithinConfiguredScratch(t *testing.T) {
	scratch := t.TempDir()
	e := &Executor{DryRunTimeout: 10 * time.Second, DryRunScratch: scratch}
	res, err := e.DryRun(context.Background(), "#!/bin/bash\nrm -f "+scratch+"/f\n")
	require.NoError(t, err)
	require.True(t, res.Approved, "reasons: %v", res.Reasons)
}

func TestExecutor_Run_RefusesBootWrite(t *testing.T) {
	// Stands a temp dir in for /boot: bind+remount-ro genuinely happens at
	// the kernel level (verified independently: unshare --user
	// --map-root-user --mount + bind + remount,ro blocks writes on this
	// host). The script's own write then fails for real — this is the
	// executor-enforced guard, not a convention.
	protected := t.TempDir()
	victim := filepath.Join(protected, "authorized_keys")
	require.NoError(t, os.WriteFile(victim, []byte("original"), 0o600))

	e := &Executor{RunTimeout: 10 * time.Second, ProtectedPaths: []string{protected}}
	res, err := e.Run(context.Background(), "#!/bin/bash\necho pwned > "+victim+"\n")
	require.NoError(t, err)
	require.False(t, res.Succeeded, "output: %s", res.Output)
	require.Contains(t, strings.ToLower(res.Output), "read-only file system")

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	require.Equal(t, "original", string(got), "the guard failed: the file was actually overwritten")
}

func TestExecutor_Run_AllowsWriteOutsideProtectedPath(t *testing.T) {
	protected := t.TempDir()
	scratch := t.TempDir()
	target := filepath.Join(scratch, "out.txt")

	e := &Executor{RunTimeout: 10 * time.Second, ProtectedPaths: []string{protected}}
	res, err := e.Run(context.Background(), "#!/bin/bash\necho real-write > "+target+"\n")
	require.NoError(t, err)
	require.True(t, res.Succeeded, "output: %s", res.Output)

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "real-write\n", string(got))
}

func TestExecutor_Run_FailsClosed_WhenGuardCannotBeEstablished(t *testing.T) {
	// A protected path that does not exist can never be bind-mounted —
	// Run must refuse to execute rather than silently run unguarded.
	e := &Executor{RunTimeout: 5 * time.Second, ProtectedPaths: []string{"/nonexistent-for-test/boot"}}
	res, err := e.Run(context.Background(), "#!/bin/bash\necho should-never-run\n")
	require.Nil(t, res)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrProtectedPathGuardFailed))
}
