package unraid

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// withRealEmhttpFixtures points emhttpDir at the committed, trimmed-but-real
// capture from rijkaardserver (testdata/{disks,shares,sec,var}.ini) rather
// than synthetic content, so these tests exercise the actual field shapes
// the fallback readers must handle.
func withRealEmhttpFixtures(t *testing.T) {
	t.Helper()
	prev := emhttpDir
	emhttpDir = "testdata"
	t.Cleanup(func() { emhttpDir = prev })
}

func TestReadArrayStateFromEmhttp(t *testing.T) {
	withRealEmhttpFixtures(t)

	state, err := readArrayStateFromEmhttp(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "STARTED", state.State, "State must pass through mdState verbatim")
	assert.False(t, state.ParityCheckActive, `mdResync="0" means no check is currently running`)
	assert.Zero(t, state.ParityCheckProgress, "progress is only meaningful while active; must not be fabricated while idle")
	assert.Equal(t, int64(0), state.ParityLastErrors)
	require.NotNil(t, state.ParityLastCheck)
	assert.Equal(t, time.Unix(1786244401, 0).UTC(), *state.ParityLastCheck, "last check date must come from sbSynced2 (completion time), not sbSynced (start time)")
	assert.Equal(t, core.SourceEmhttp, state.Source, "array state read from emhttp must be tagged emhttp, never left blank or claimed as unraid-api")

	byName := map[string]core.Disk{}
	for _, d := range state.Disks {
		byName[d.Name] = d
	}
	// disk2 (device="") is an unpopulated array slot and flash is the boot
	// device: neither belongs in the array's disk list, and including them
	// would either fabricate a phantom disk or misrepresent the boot device
	// as an array member.
	assert.NotContains(t, byName, "disk2", "an unpopulated array slot must not appear as a fabricated zero-size disk")
	assert.NotContains(t, byName, "flash", "the boot device is outside GraphQL's array.{disks,caches,parities} scope")
	require.Len(t, state.Disks, 4, "expected parity, disk1, disk3, cache")

	parity := byName["parity"]
	assert.Equal(t, "/dev/sdc", parity.Device)
	assert.Equal(t, "parity", parity.Role)
	assert.Equal(t, int64(11718885324)*1024, parity.SizeBytes)
	assert.Equal(t, int64(0), parity.UsedBytes, "parity has no filesystem: disks.ini's parity stanza carries no fsUsed key at all, so this must stay the genuine zero, not a guess")
	require.NotNil(t, parity.TempC)
	assert.Equal(t, 44, *parity.TempC)
	assert.Equal(t, "OK", parity.SmartStatus)
	assert.False(t, parity.SpunDown)

	disk1 := byName["disk1"]
	assert.Equal(t, "/dev/sdd", disk1.Device)
	assert.Equal(t, "data", disk1.Role)
	assert.Equal(t, int64(4702947328)*1024, disk1.UsedBytes, "disk1 does have fsUsed in the real capture and must use it")
	require.NotNil(t, disk1.TempC)
	assert.Equal(t, 40, *disk1.TempC)

	cache := byName["cache"]
	assert.Equal(t, "cache", cache.Role)
	require.NotNil(t, cache.TempC)
	assert.Equal(t, 32, *cache.TempC)
}

func TestReadArrayStateFromEmhttp_MissingFile(t *testing.T) {
	prev := emhttpDir
	emhttpDir = t.TempDir() // empty: no var.ini, no disks.ini
	t.Cleanup(func() { emhttpDir = prev })

	_, err := readArrayStateFromEmhttp(context.Background())
	require.Error(t, err, "a missing ini file must be a read error, never an empty-but-successful result")
}

func TestReadSharesFromEmhttp(t *testing.T) {
	withRealEmhttpFixtures(t)

	shares, err := readSharesFromEmhttp(context.Background())
	require.NoError(t, err)
	require.Len(t, shares, 3)

	byName := map[string]core.Share{}
	for _, sh := range shares {
		byName[sh.Name] = sh
	}

	media := byName["Media"]
	assert.Equal(t, "highwater", media.Allocator)
	assert.Equal(t, "cache", media.CachePool)
	assert.Equal(t, int64(364476616)*1024, media.FreeBytes)
	assert.Equal(t, int64(132131768)*1024, media.UsedBytes)
	assert.False(t, media.Exported, `export="-" means not exported`)
	assert.Equal(t, core.SourceEmhttp, media.Source, "shares read from emhttp must be tagged emhttp")

	printing := byName["3D Printing"]
	assert.True(t, printing.Exported, `export="e" means exported`)

	appdata := byName["appdata"]
	assert.True(t, appdata.Exported)
	assert.Equal(t, "cache", appdata.CachePool)
}
