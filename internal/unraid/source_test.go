package unraid

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"server-assistant/internal/config"
	"server-assistant/internal/core"
)

// loadUnraidConfig writes a minimal config.yaml pointing `unraid:` at
// graphqlURL/smartctlPath/tailscalePath/dockerSocket and loads it through
// the real config.FileSource — exercising UnraidConfig's own (unexported)
// resolve()/default-timeout logic rather than hand-building a struct that
// would skip it.
func loadUnraidConfig(t *testing.T, graphqlURL, smartctlPath, tailscalePath, dockerSocket string) config.UnraidConfig {
	t.Helper()
	yaml := fmt.Sprintf(`schema_version: 1
unraid:
  graphql_url: %q
  api_key: test-key
  smartctl_path: %q
  tailscale_path: %q
  docker_socket: %q
`, graphqlURL, smartctlPath, tailscalePath, dockerSocket)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))

	cfg, err := config.NewFileSource(path).Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, cfg.Unraid)
	return *cfg.Unraid
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func TestSource_HostInfo_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := loadUnraidConfig(t, srv.URL, "smartctl", "tailscale", "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	_, err := src.HostInfo(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrUnauthenticated, "an unauthenticated GraphQL read must surface core.ErrUnauthenticated, never a zero HostInfo")
}

func TestSource_Array_MapsDisksAndSmartStatusAcrossQueries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{
			"array": {
				"state": "STARTED",
				"parityCheckStatus": {"running": false, "progress": null, "date": null, "errors": null},
				"disks": [{"name":"disk1","device":"sdd","size":"5860522532","status":"DISK_OK","rotational":true,"temp":40,"fsSize":"5858435620","fsFree":"1155488664","fsUsed":"4702946956","type":"DATA","isSpinning":true}],
				"caches": [],
				"parities": [{"name":"parity","device":"sdc","size":"11718885324","status":"DISK_OK","rotational":true,"temp":44,"fsSize":"0","fsFree":"0","fsUsed":"0","type":"PARITY","isSpinning":false}]
			},
			"disks": [{"device":"/dev/sdd","smartStatus":"OK"}]
		}}`))
	}))
	defer srv.Close()

	cfg := loadUnraidConfig(t, srv.URL, "smartctl", "tailscale", "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	array, err := src.Array(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "STARTED", array.State)
	assert.Equal(t, core.SourceUnraidAPI, array.Source, "a successful GraphQL read must be tagged unraid-api")
	require.Len(t, array.Disks, 2)

	byName := map[string]core.Disk{}
	for _, d := range array.Disks {
		byName[d.Name] = d
	}

	disk1 := byName["disk1"]
	assert.Equal(t, "/dev/sdd", disk1.Device, "bare device names must be normalized to smartctl's /dev/ form")
	assert.Equal(t, "data", disk1.Role)
	assert.Equal(t, "OK", disk1.SmartStatus, "smartStatus from the separate top-level disks{} query must merge in by device")
	assert.False(t, disk1.SpunDown)
	require.NotNil(t, disk1.TempC)
	assert.Equal(t, 40, *disk1.TempC)
	assert.Equal(t, int64(5860522532)*1024, disk1.SizeBytes, "size must be converted from KiB to bytes")

	parity := byName["parity"]
	assert.Equal(t, "parity", parity.Role)
	assert.True(t, parity.SpunDown)
	assert.Equal(t, "UNKNOWN", parity.SmartStatus, "no match in the disks{} query must fall back to the enum's own UNKNOWN, not a fabricated value")
}

func TestSource_SmartFor_Standby(t *testing.T) {
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "smartctl")
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 2\n"), 0o755))

	cfg := loadUnraidConfig(t, "http://127.0.0.1:1/graphql", scriptPath, "tailscale", "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	_, err := src.SmartFor(context.Background(), "/dev/sdd")
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrDiskStandby)
}

func TestSource_Reachability_Absent(t *testing.T) {
	cfg := loadUnraidConfig(t, "http://127.0.0.1:1/graphql", "smartctl", filepath.Join(t.TempDir(), "no-tailscale"), "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	r, err := src.Reachability(context.Background())
	require.NoError(t, err)
	assert.Equal(t, core.ReachAbsent, r.State)
}

func TestSource_Array_FallsBackToEmhttp_OnUnauthenticated(t *testing.T) {
	withRealEmhttpFixtures(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := loadUnraidConfig(t, srv.URL, "smartctl", "tailscale", "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	array, err := src.Array(context.Background())
	require.NoError(t, err, "an unauthenticated GraphQL read must fall back to emhttp, not surface as an error")
	assert.Equal(t, core.SourceEmhttp, array.Source, "the fallback read must be tagged emhttp so the dashboard can tell it apart from a full unraid-api read")
	assert.Equal(t, "STARTED", array.State)
	assert.NotEmpty(t, array.Disks, "the emhttp fallback must still surface real disks, not an empty array")
}

func TestSource_Array_NonAuthGraphQLError_DoesNotFallBack(t *testing.T) {
	// emhttpDir points nowhere real: if Array() incorrectly fell back on a
	// non-auth error, this read would fail with a file-not-found style
	// error instead of surfacing the original GraphQL message below.
	prevDir := emhttpDir
	emhttpDir = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { emhttpDir = prevDir })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"boom: internal resolver error"}]}`))
	}))
	defer srv.Close()

	cfg := loadUnraidConfig(t, srv.URL, "smartctl", "tailscale", "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	array, err := src.Array(context.Background())
	require.Error(t, err, "a non-auth GraphQL error must surface as an error, never be swallowed by a fallback")
	assert.False(t, errors.Is(err, core.ErrUnauthenticated), "this error is not an auth failure and must not be mistaken for one")
	assert.Contains(t, err.Error(), "boom: internal resolver error", "the original GraphQL error must reach the caller, not an emhttp-fallback error")
	assert.Equal(t, core.ArrayState{}, array, "no partial or fallback data on a real failure")
}

func TestSource_Shares_MapsFromGraphQL_ProvenanceUnraidAPI(t *testing.T) {
	withRealEmhttpFixtures(t) // shares.ini/sec.ini gap-fill (cachePool/exported) for the GraphQL path

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"shares":[{"name":"Media","size":"0","free":"364476616","used":"132131768","allocator":"highwater"}]}}`))
	}))
	defer srv.Close()

	cfg := loadUnraidConfig(t, srv.URL, "smartctl", "tailscale", "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	shares, err := src.Shares(context.Background())
	require.NoError(t, err)
	require.Len(t, shares, 1)
	assert.Equal(t, "Media", shares[0].Name)
	assert.Equal(t, "cache", shares[0].CachePool, "cachePool is GraphQL's own gap, filled from shares.ini")
	assert.False(t, shares[0].Exported, `sec.ini export="-" means not exported`)
	assert.Equal(t, core.SourceUnraidAPI, shares[0].Source, "a successful GraphQL read must be tagged unraid-api")
}

func TestSource_Shares_FallsBackToEmhttp_OnUnauthenticated(t *testing.T) {
	withRealEmhttpFixtures(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cfg := loadUnraidConfig(t, srv.URL, "smartctl", "tailscale", "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	shares, err := src.Shares(context.Background())
	require.NoError(t, err, "an unauthenticated GraphQL read must fall back to emhttp, not surface as an error")
	require.NotEmpty(t, shares)
	for _, sh := range shares {
		assert.Equal(t, core.SourceEmhttp, sh.Source, "every share from the fallback path must be tagged emhttp")
	}
}

func TestSource_Shares_NonAuthGraphQLError_DoesNotFallBack(t *testing.T) {
	prevDir := emhttpDir
	emhttpDir = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { emhttpDir = prevDir })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"boom: internal resolver error"}]}`))
	}))
	defer srv.Close()

	cfg := loadUnraidConfig(t, srv.URL, "smartctl", "tailscale", "/no/such/sock")
	src := NewSource(cfg, ":8090", discardLogger())

	shares, err := src.Shares(context.Background())
	require.Error(t, err, "a non-auth GraphQL error must surface as an error, never be swallowed by a fallback")
	assert.False(t, errors.Is(err, core.ErrUnauthenticated))
	assert.Contains(t, err.Error(), "boom: internal resolver error")
	assert.Nil(t, shares, "no partial or fallback data on a real failure")
}
