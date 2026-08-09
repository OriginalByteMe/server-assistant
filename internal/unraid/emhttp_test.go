package unraid

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseINI(t *testing.T) {
	// Trimmed from a real capture of /var/local/emhttp/disks.ini on
	// rijkaardserver (docs/research/unraid-state-sources.md).
	const sample = `["parity"]
idx="0"
name="parity"
device="sdc"
spundown="0"
temp="44"

["disk1"]
idx="1"
name="disk1"
device="sdd"
spundown="0"
temp="40"
`
	sections, err := parseINI(strings.NewReader(sample))
	require.NoError(t, err)
	require.Contains(t, sections, "parity")
	require.Contains(t, sections, "disk1")
	assert.Equal(t, "sdc", sections["parity"]["device"])
	assert.Equal(t, "44", sections["parity"]["temp"])
	assert.Equal(t, "sdd", sections["disk1"]["device"])
}

func TestParseINI_StrayLineOutsideSection(t *testing.T) {
	sections, err := parseINI(strings.NewReader("key=\"value\"\n[\"a\"]\nkey=\"in-section\"\n"))
	require.NoError(t, err)
	assert.Len(t, sections, 1)
	assert.Equal(t, "in-section", sections["a"]["key"])
}

func withEmhttpFixtures(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	prevDir := emhttpDir
	emhttpDir = dir
	t.Cleanup(func() { emhttpDir = prevDir })
}

func TestReadShareGaps(t *testing.T) {
	// Trimmed from real captures of shares.ini/sec.ini on rijkaardserver.
	withEmhttpFixtures(t, map[string]string{
		"shares.ini": `["Media"]
name="Media"
cachePool="cache"

["VMs"]
name="VMs"
cachePool=""
`,
		"sec.ini": `["Media"]
export="-"
security="public"

["VMs"]
export="e"
security="public"
`,
	})

	gaps, err := readShareGaps(context.Background())
	require.NoError(t, err)

	require.Contains(t, gaps, "Media")
	assert.Equal(t, "cache", gaps["Media"].cachePool)
	assert.False(t, gaps["Media"].exported, "export=\"-\" means not exported")

	require.Contains(t, gaps, "VMs")
	assert.Equal(t, "", gaps["VMs"].cachePool)
	assert.True(t, gaps["VMs"].exported, "export=\"e\" means exported")
}

func TestReadShareGaps_MissingFile(t *testing.T) {
	withEmhttpFixtures(t, map[string]string{}) // neither file present
	_, err := readShareGaps(context.Background())
	require.Error(t, err, "a missing ini file must be a read error, never an empty-but-successful result")
}

func TestReadINI_ContextCanceled(t *testing.T) {
	withEmhttpFixtures(t, map[string]string{"var.ini": `["a"]` + "\n"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := readINI(ctx, "var.ini")
	require.Error(t, err)
}

func TestShareAccessible(t *testing.T) {
	dir := t.TempDir()
	prevRoot := userSharesRoot
	userSharesRoot = dir
	t.Cleanup(func() { userSharesRoot = prevRoot })

	require.NoError(t, os.Mkdir(filepath.Join(dir, "Media"), 0o755))

	assert.True(t, shareAccessible("Media"))
	assert.False(t, shareAccessible("DoesNotExist"))
}
