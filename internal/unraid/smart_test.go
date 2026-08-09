package unraid

// The fixture at testdata/smartctl_disk1.json is a REAL `smartctl -n standby
// -i -A -j /dev/sdd` capture taken read-only from rijkaardserver's disk1
// (WDC WD60EFPX-68C5ZN0) on 2026-08-09, with `model_name`/`serial_number`
// replaced by synthetic placeholders and the device path genericized to
// "/dev/sdX" — the attribute table, temperature, power-on hours etc. are
// exactly what the drive reported. The real serial was never committed here:
// core.go requires this source never store or log it, and committing a
// physical drive's serial to source control is unnecessary exposure the
// same principle argues against even in a fixture.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// TestSmartctlArgs_AlwaysPassesStandbyFlag is the regression test the ticket
// requires: it fails if "-n standby" is ever dropped from the invocation.
func TestSmartctlArgs_AlwaysPassesStandbyFlag(t *testing.T) {
	args := smartctlArgs("/dev/sdd")
	require.GreaterOrEqual(t, len(args), 2)
	assert.Equal(t, "-n", args[0], "the standby-skip flag must always be first")
	assert.Equal(t, "standby", args[1], "the standby-skip flag's value must always follow -n")
	assert.Contains(t, args, "-A", "raw attribute table must still be requested")
}

// fakeSmartctl writes an executable shell script to a temp dir that ignores
// its arguments and prints body to stdout, exiting with code. It returns the
// script's path, suitable as SmartctlPath.
func fakeSmartctl(t *testing.T, body string, code int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "smartctl")
	script := fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\nexit %d\n", body, code)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// fakeSmartctlAssertingStandby is the same as fakeSmartctl, but the script
// itself fails (nonzero exit distinct from the standby exit code) unless its
// argv literally contains "-n standby" adjacently — an end-to-end guard, in
// addition to TestSmartctlArgs_AlwaysPassesStandbyFlag, that runSmartctl
// really does pass the flags it builds through to the child process.
func fakeSmartctlAssertingStandby(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "smartctl")
	script := `#!/bin/sh
seen_n=0
for a in "$@"; do
  if [ "$prev" = "-n" ] && [ "$a" = "standby" ]; then
    seen_n=1
  fi
  prev="$a"
done
if [ "$seen_n" != "1" ]; then
  echo "FAIL: -n standby not found in argv: $@" >&2
  exit 99
fi
cat <<'EOF'
` + body + `
EOF
exit 0
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func loadFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "smartctl_disk1.json"))
	require.NoError(t, err)
	return string(b)
}

func TestRunSmartctl_Success(t *testing.T) {
	fixture := loadFixture(t)
	path := fakeSmartctlAssertingStandby(t, fixture)

	attrs, err := runSmartctl(context.Background(), path, "/dev/sdX")
	require.NoError(t, err)

	assert.Equal(t, "/dev/sdX", attrs.Device)
	assert.Equal(t, "WDC WD60EFPX-68C5ZN0", attrs.ModelName)
	assert.NotEmpty(t, attrs.SerialHash)
	assert.Len(t, attrs.SerialHash, 16)
	assert.NotEmpty(t, attrs.Attributes)

	// A couple of attributes present in the real capture, spot-checked.
	var found194 bool
	for _, a := range attrs.Attributes {
		if a.ID == 194 {
			found194 = true
			assert.Equal(t, "Temperature_Celsius", a.Name)
		}
	}
	assert.True(t, found194, "expected attribute 194 (Temperature_Celsius) from the real capture")
}

// TestRunSmartctl_NeverLeaksRawSerial covers the ticket's explicit
// requirement: the raw serial from smartctl's JSON must never appear
// anywhere in this source's output, only its hash.
func TestRunSmartctl_NeverLeaksRawSerial(t *testing.T) {
	const rawSerial = "WD-SYNTHETIC0000001" // matches testdata/smartctl_disk1.json
	fixture := loadFixture(t)
	require.Contains(t, fixture, rawSerial, "sanity: the fixture must actually contain a serial for this test to mean anything")

	path := fakeSmartctl(t, fixture, 0)
	attrs, err := runSmartctl(context.Background(), path, "/dev/sdX")
	require.NoError(t, err)

	out, err := json.Marshal(attrs)
	require.NoError(t, err)
	assert.NotContains(t, string(out), rawSerial)
	assert.NotContains(t, fmt.Sprintf("%+v", attrs), rawSerial)

	expectedHash := hashSerial(rawSerial)
	assert.Equal(t, expectedHash, attrs.SerialHash)
	assert.NotEqual(t, rawSerial, attrs.SerialHash)
}

func TestRunSmartctl_StandbyExitCodeMapsToErrDiskStandby(t *testing.T) {
	// Real standby-skip output is not reproduced on the reference host (no
	// disk was observed asleep — docs/research/unraid-state-sources.md,
	// "Does reading SMART spin up a sleeping disk?"); the exit-code
	// contract is documented externally (smartmontools EXIT STATUS) and is
	// what runSmartctl actually keys off, so the fixture body here is
	// deliberately minimal — only the exit code is under test.
	path := fakeSmartctl(t, `{"smartctl":{"exit_status":2}}`, 2)

	_, err := runSmartctl(context.Background(), path, "/dev/sdX")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrDiskStandby)
}

func TestRunSmartctl_OtherNonZeroExitIsNotStandby(t *testing.T) {
	// Exit code 1 (command-line/other failure, standby bit not set) must not
	// be misreported as "disk asleep".
	path := fakeSmartctl(t, `{}`, 1)

	_, err := runSmartctl(context.Background(), path, "/dev/sdX")
	require.Error(t, err)
	assert.False(t, errors.Is(err, core.ErrDiskStandby))
}

func TestRunSmartctl_BinaryMissing(t *testing.T) {
	_, err := runSmartctl(context.Background(), filepath.Join(t.TempDir(), "no-such-binary"), "/dev/sdX")
	require.Error(t, err)
	var exitErr *exec.ExitError
	assert.False(t, errors.As(err, &exitErr), "a missing binary is not an ExitError")
}

func TestHashSerial_Deterministic(t *testing.T) {
	h1 := hashSerial("WD-ABC123")
	h2 := hashSerial("WD-ABC123")
	h3 := hashSerial("WD-XYZ789")
	assert.Equal(t, h1, h2)
	assert.NotEqual(t, h1, h3)
	assert.Len(t, h1, 16)
	assert.NotContains(t, h1, "WD-ABC123")
}
