package unraid

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/config"
)

// procTestdata is testdata/proc: a trimmed-but-real capture from
// rijkaardserver's HOST /proc (meminfo/uptime/stat/cpuinfo), so these tests
// exercise the actual field shapes the readers must handle, same rationale
// as withRealEmhttpFixtures for the INI fallback.
const procTestdata = "testdata/proc"

func TestReadMemInfo_UsesMemAvailableNotMemFree(t *testing.T) {
	total, used, err := readMemInfo(procTestdata)
	require.NoError(t, err)

	// From the captured fixture: MemTotal=32785840 kB, MemAvailable=19920172
	// kB, both KiB despite the "kB" label -> x1024 for bytes.
	assert.Equal(t, int64(32785840)*1024, total)
	assert.Equal(t, int64(32785840-19920172)*1024, used)

	// MemFree in the same fixture is 636232 kB, which would put "used" at
	// ~32.1M kB (98% of the box) if MemFree were used instead of
	// MemAvailable — a page-cache-inflated figure this fixture's used value
	// must NOT match.
	usedIfMemFreeWereUsed := int64(32785840-636232) * 1024
	assert.NotEqual(t, usedIfMemFreeWereUsed, used, "must use MemAvailable, not MemFree, or usage is wildly overstated by page cache")
}

func TestReadMemInfo_MissingFile(t *testing.T) {
	_, _, err := readMemInfo(t.TempDir())
	require.Error(t, err, "a missing meminfo must be a read error, never a fabricated zero")
}

func TestReadUptimeSeconds(t *testing.T) {
	seconds, err := readUptimeSeconds(procTestdata)
	require.NoError(t, err)
	// Fixture's first field is "1470259.45".
	assert.Equal(t, int64(1470259), seconds)
}

func TestReadCPUInfo(t *testing.T) {
	model, cores, err := readCPUInfo(procTestdata)
	require.NoError(t, err)
	assert.Equal(t, "Intel(R) Xeon(R) CPU E5-2680 v4 @ 2.40GHz", model)
	assert.Equal(t, 28, cores)
}

func TestReadCPUStatSample(t *testing.T) {
	sample, err := readCPUStatSample(procTestdata)
	require.NoError(t, err)
	// Fixture's aggregate line: user=609510117 nice=129350247
	// system=80302805 idle=3204654165 iowait=63228368 irq=0 softirq=3082871
	// steal=0 guest=0 guest_nice=0.
	assert.Equal(t, uint64(3204654165+63228368), sample.idle, "idle bucket must be idle+iowait")
	wantTotal := uint64(609510117 + 129350247 + 80302805 + 3204654165 + 63228368 + 0 + 3082871 + 0 + 0 + 0)
	assert.Equal(t, wantTotal, sample.total)
}

func TestCPUPercentFromSamples(t *testing.T) {
	tests := []struct {
		name    string
		first   cpuStatSample
		second  cpuStatSample
		want    float64
		wantErr bool
	}{
		{
			name:   "half busy",
			first:  cpuStatSample{idle: 1000, total: 10000},
			second: cpuStatSample{idle: 1500, total: 11000},
			want:   50.0, // idleDelta=500, totalDelta=1000 -> 100*(1-0.5)
		},
		{
			name:   "fully idle",
			first:  cpuStatSample{idle: 1000, total: 10000},
			second: cpuStatSample{idle: 2000, total: 11000},
			want:   0.0,
		},
		{
			name:   "fully busy",
			first:  cpuStatSample{idle: 1000, total: 10000},
			second: cpuStatSample{idle: 1000, total: 11000},
			want:   100.0,
		},
		{
			name:    "zero total delta must error, not fabricate 0.0",
			first:   cpuStatSample{idle: 1000, total: 10000},
			second:  cpuStatSample{idle: 1000, total: 10000},
			wantErr: true,
		},
		{
			name:    "negative total delta (counter went backwards) must error",
			first:   cpuStatSample{idle: 1000, total: 10000},
			second:  cpuStatSample{idle: 900, total: 9000},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cpuPercentFromSamples(tc.first, tc.second)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, got, 0.0001)
		})
	}
}

func TestHostInfoFromProcfs_MissingOrUnreadablePath_Errors(t *testing.T) {
	// A nonexistent path must be a hard error, never a silent fallback to
	// the container's own real /proc (which exists on any Linux test
	// runner and would otherwise make this look like a passing read).
	cfg := config.UnraidConfig{HostProcPath: filepath.Join(t.TempDir(), "does-not-exist")}
	_, err := hostInfoFromProcfs(context.Background(), cfg)
	require.Error(t, err)
}

func TestHostInfoFromProcfs_EmptyPath_Errors(t *testing.T) {
	// An unconfigured (zero-value) HostProcPath must also error rather than
	// defaulting to the container's own "/proc" inside this function —
	// resolve() supplies the "/host/proc" default; this function itself
	// must never guess a path.
	cfg := config.UnraidConfig{}
	_, err := hostInfoFromProcfs(context.Background(), cfg)
	require.Error(t, err)
}

// withLiveStatFixture builds a temp procfs root by copying the static
// meminfo/uptime/cpuinfo fixtures verbatim and starting a background writer
// that rewrites stat's aggregate cpu line every 2ms. The real captured
// stat fixture is static (a single point-in-time snapshot), so two reads of
// it back to back are always identical — correctly triggering the
// zero-delta guard, not a bug. Full hostInfoFromProcfs orchestration tests
// need a stat file that genuinely changes between the two samples
// readCPUPercent takes, the same way the real host's /proc/stat does.
func withLiveStatFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"meminfo", "uptime", "cpuinfo"} {
		data, err := os.ReadFile(filepath.Join(procTestdata, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o644))
	}
	statPath := filepath.Join(dir, "stat")
	require.NoError(t, os.WriteFile(statPath, []byte("cpu  0 0 0 0 0 0 0 0 0 0\n"), 0o644))

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		var n uint64
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				n++
				line := fmt.Sprintf("cpu  %d 0 0 %d 0 0 0 0 0 0\n", n, n*9)
				tmp := statPath + ".tmp"
				if err := os.WriteFile(tmp, []byte(line), 0o644); err == nil {
					_ = os.Rename(tmp, statPath) // atomic on the same filesystem: never a torn read
				}
			}
		}
	}()
	return dir
}

func loadTestUnraidConfig(t *testing.T, hostProcPath, cpuSampleInterval string) config.UnraidConfig {
	t.Helper()
	yaml := fmt.Sprintf(`schema_version: 1
unraid:
  host_proc_path: %q
  cpu_sample_interval: %q
`, hostProcPath, cpuSampleInterval)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))
	cfg, err := config.NewFileSource(path).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cfg.Unraid)
	return *cfg.Unraid
}

func TestHostInfoFromProcfs_UsesConfiguredPathNotContainerProc(t *testing.T) {
	// procTestdata's MemTotal (32785840 kB, computed to bytes below) is
	// rijkaardserver's captured value. If this function read the real
	// system /proc instead of cfg.HostProcPath, MemTotalBytes would not
	// equal this fixture's figure on any machine other than that exact
	// host — the strongest available proof the configured path, not the
	// container's own /proc, was actually read.
	dir := withLiveStatFixture(t)
	cfg := loadTestUnraidConfig(t, dir, "20ms")
	info, err := hostInfoFromProcfs(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, int64(32785840)*1024, info.MemTotalBytes)
	assert.Equal(t, 28, info.CPUCores)
	assert.GreaterOrEqual(t, info.CPUPercent, 0.0)
}

func TestReadCPUPercent_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readCPUPercent(ctx, procTestdata, time.Second)
	require.Error(t, err, "a canceled context must abort the inter-sample wait, not silently sample anyway")
}
