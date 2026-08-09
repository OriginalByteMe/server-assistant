package unraid

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"server-assistant/internal/config"
	"server-assistant/internal/core"
)

// Source is the concrete core.UnraidSource (HL-SA-22): it fans each method
// out to the collector that owns its subject — GraphQL for host/array/share
// state, smartctl for raw SMART attributes, the Docker socket for
// containers, the tailscale CLI for reachability — each bounded by its own
// configured timeout (CONVENTIONS rule 4).
//
// Every field is either immutable after construction or itself an
// *http.Client (safe for concurrent use by design), so Source needs no
// locking of its own to satisfy the interface's "safe for concurrent use"
// requirement: the dashboard, the MCP endpoint and the sampler share one
// instance without coordinating.
type Source struct {
	cfg    config.UnraidConfig
	gql    *graphqlClient
	docker *DockerClient
	reach  *reachabilityChecker
	log    *slog.Logger
}

var _ core.UnraidSource = (*Source)(nil)

// NewSource builds the composite Unraid state source. dashboardAddr is the
// dashboard's own HTTP listen address (Config.HTTPAddr, e.g. ":8090") — the
// reachability self-check needs its own port to find itself in the live
// tailscale serve/funnel config. log must not be nil; pass slog.Default() if
// the caller has no dedicated logger.
func NewSource(cfg config.UnraidConfig, dashboardAddr string, log *slog.Logger) *Source {
	return &Source{
		cfg:    cfg,
		gql:    newGraphQLClient(cfg.GraphQLURL, cfg.APIKey),
		docker: NewDockerClient(cfg.DockerSocket),
		reach:  newReachabilityChecker(cfg.TailscalePath, dashboardAddr),
		log:    log,
	}
}

// HostInfo reads host CPU/memory/uptime, falling back to the host's
// bind-mounted procfs (HL-SA-22, procfs.go) when and only when unraid-api
// rejects the credential (errors.Is core.ErrUnauthenticated) — same rule as
// Array/Shares. Any other GraphQL failure still surfaces as an error.
func (s *Source) HostInfo(ctx context.Context) (core.HostInfo, error) {
	info, err := s.hostInfoFromGraphQL(ctx)
	if err == nil {
		return info, nil
	}
	if !errors.Is(err, core.ErrUnauthenticated) {
		return core.HostInfo{}, err
	}
	s.log.InfoContext(ctx, "unraid host info read from procfs: unraid-api credential absent")
	info, ferr := hostInfoFromProcfs(ctx, s.cfg)
	if ferr != nil {
		s.log.ErrorContext(ctx, "unraid host info procfs fallback read failed", "error", ferr)
		return core.HostInfo{}, fmt.Errorf("unraid host info: procfs fallback: %w", ferr)
	}
	return info, nil
}

// hostInfoFromGraphQL is the full-fidelity path: unraid-api's info/vars/
// metrics query.
func (s *Source) hostInfoFromGraphQL(ctx context.Context) (core.HostInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GraphQLTimeout())
	defer cancel()

	var resp hostInfoResponse
	if err := s.gql.do(ctx, hostInfoQuery, &resp); err != nil {
		if !errors.Is(err, core.ErrUnauthenticated) {
			s.log.ErrorContext(ctx, "unraid host info read failed", "error", err)
		}
		return core.HostInfo{}, fmt.Errorf("unraid host info: %w", err)
	}

	uptime, err := parseUptimeSeconds(resp.Info.OS.Uptime)
	if err != nil {
		return core.HostInfo{}, fmt.Errorf("unraid host info: %w", err)
	}

	model := resp.Info.CPU.Brand
	if model == "" {
		model = strings.TrimSpace(resp.Info.CPU.Manufacturer)
	}

	return core.HostInfo{
		Hostname:      resp.Info.OS.Hostname,
		UnraidVersion: resp.Vars.Version,
		CPUModel:      model,
		CPUCores:      resp.Info.CPU.Cores,
		CPUPercent:    resp.Metrics.CPU.PercentTotal,
		MemTotalBytes: int64(resp.Metrics.Memory.Total),
		MemUsedBytes:  int64(resp.Metrics.Memory.Used),
		UptimeSeconds: uptime,
		Source:        core.SourceUnraidAPI,
		CollectedAt:   time.Now(),
	}, nil
}

// Array reads array/parity/disk state, falling back to /var/local/emhttp's
// INI files (HL-SA-22) when and only when unraid-api rejects the
// credential (errors.Is core.ErrUnauthenticated) — any other GraphQL
// failure still surfaces as an error rather than silently degrading.
func (s *Source) Array(ctx context.Context) (core.ArrayState, error) {
	array, err := s.arrayFromGraphQL(ctx)
	if err == nil {
		return array, nil
	}
	if !errors.Is(err, core.ErrUnauthenticated) {
		return core.ArrayState{}, err
	}
	return s.arrayFromEmhttp(ctx)
}

// arrayFromGraphQL is the full-fidelity path: unraid-api's Array/parity/disk
// query plus the separate top-level disks{} query for smartStatus.
func (s *Source) arrayFromGraphQL(ctx context.Context) (core.ArrayState, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GraphQLTimeout())
	defer cancel()

	var resp arrayResponse
	if err := s.gql.do(ctx, arrayQuery, &resp); err != nil {
		if !errors.Is(err, core.ErrUnauthenticated) {
			s.log.ErrorContext(ctx, "unraid array read failed", "error", err)
		}
		return core.ArrayState{}, fmt.Errorf("unraid array: %w", err)
	}

	smartStatus := make(map[string]string, len(resp.Disks))
	for _, d := range resp.Disks {
		smartStatus[normalizeDevice(d.Device)] = d.SmartStatus
	}

	var disks []core.Disk
	disks = append(disks, mapArrayDisks(resp.Array.Disks, smartStatus)...)
	disks = append(disks, mapArrayDisks(resp.Array.Caches, smartStatus)...)
	disks = append(disks, mapArrayDisks(resp.Array.Parities, smartStatus)...)

	var lastCheck *time.Time
	if resp.Array.ParityCheckStatus.Date != nil && *resp.Array.ParityCheckStatus.Date != "" {
		if t, err := time.Parse(time.RFC3339, *resp.Array.ParityCheckStatus.Date); err == nil {
			lastCheck = &t
		}
	}
	var progress float64
	if resp.Array.ParityCheckStatus.Progress != nil {
		progress = float64(*resp.Array.ParityCheckStatus.Progress)
	}
	var lastErrors int64
	if resp.Array.ParityCheckStatus.Errors != nil {
		lastErrors = *resp.Array.ParityCheckStatus.Errors
	}

	return core.ArrayState{
		State:               resp.Array.State,
		ParityCheckActive:   resp.Array.ParityCheckStatus.Running,
		ParityCheckProgress: progress,
		ParityLastCheck:     lastCheck,
		ParityLastErrors:    lastErrors,
		Source:              core.SourceUnraidAPI,
		Disks:               disks,
		CollectedAt:         time.Now(),
	}, nil
}

// arrayFromEmhttp is the no-API-key fallback: /var/local/emhttp's INI files
// (HL-SA-22). Logged once per fallback at INFO, not ERROR: a missing API
// key is an expected, diagnosable degradation, not a failure.
func (s *Source) arrayFromEmhttp(ctx context.Context) (core.ArrayState, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.EmhttpTimeout())
	defer cancel()

	s.log.InfoContext(ctx, "unraid array state read from emhttp: unraid-api credential absent")
	array, err := readArrayStateFromEmhttp(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "unraid array emhttp fallback read failed", "error", err)
		return core.ArrayState{}, fmt.Errorf("unraid array: emhttp fallback: %w", err)
	}
	return array, nil
}

// mapArrayDisks converts one array.{disks,caches,parities} fragment list to
// core.Disk. smartStatus is keyed by normalized device path, from the
// separate top-level Query.disks field — the only place GraphQL exposes the
// coarse OK/UNKNOWN verdict (confirmed via live introspection: ArrayDisk has
// no smartStatus field at all).
func mapArrayDisks(fragments []arrayDiskFragment, smartStatus map[string]string) []core.Disk {
	out := make([]core.Disk, 0, len(fragments))
	for _, d := range fragments {
		device := normalizeDevice(d.Device)
		status, ok := smartStatus[device]
		if !ok {
			status = "UNKNOWN" // the enum's own "not established" value — not fabricated
		}
		// isSpinning==nil (Unraid itself doesn't know) defaults to "assume
		// awake": a false positive standby claim would wrongly suppress a
		// SMART read gated on spun-down state, which is worse than an
		// occasional unnecessary read.
		spunDown := false
		if d.IsSpinning != nil {
			spunDown = !*d.IsSpinning
		}
		var tempC *int
		if d.Temp != nil {
			t := *d.Temp
			tempC = &t
		}
		out = append(out, core.Disk{
			Name:        d.Name,
			Device:      device,
			Role:        strings.ToLower(d.Type),
			SizeBytes:   kibToBytes(d.Size),
			UsedBytes:   kibToBytes(d.FsUsed),
			TempC:       tempC,
			SmartStatus: status,
			SpunDown:    spunDown,
		})
	}
	return out
}

// Shares reads user share state, falling back to /var/local/emhttp's INI
// files (HL-SA-22) when and only when unraid-api rejects the credential —
// same rule as Array.
func (s *Source) Shares(ctx context.Context) ([]core.Share, error) {
	shares, err := s.sharesFromGraphQL(ctx)
	if err == nil {
		return shares, nil
	}
	if !errors.Is(err, core.ErrUnauthenticated) {
		return nil, err
	}
	return s.sharesFromEmhttp(ctx)
}

func (s *Source) sharesFromGraphQL(ctx context.Context) ([]core.Share, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.GraphQLTimeout())
	defer cancel()

	var resp sharesResponse
	if err := s.gql.do(ctx, sharesQuery, &resp); err != nil {
		if !errors.Is(err, core.ErrUnauthenticated) {
			s.log.ErrorContext(ctx, "unraid shares read failed", "error", err)
		}
		return nil, fmt.Errorf("unraid shares: %w", err)
	}

	gapCtx, gapCancel := context.WithTimeout(ctx, s.cfg.EmhttpTimeout())
	defer gapCancel()
	gaps, err := readShareGaps(gapCtx)
	if err != nil {
		s.log.ErrorContext(ctx, "unraid share gap fields read failed", "error", err)
		return nil, fmt.Errorf("unraid shares: %w", err)
	}

	out := make([]core.Share, 0, len(resp.Shares))
	for _, sh := range resp.Shares {
		g := gaps[sh.Name]
		out = append(out, core.Share{
			Name:       sh.Name,
			SizeBytes:  kibToBytes(sh.Size),
			FreeBytes:  kibToBytes(sh.Free),
			UsedBytes:  kibToBytes(sh.Used),
			Allocator:  sh.Allocator,
			CachePool:  g.cachePool,
			Exported:   g.exported,
			Accessible: shareAccessible(sh.Name),
			Source:     core.SourceUnraidAPI,
		})
	}
	return out, nil
}

// sharesFromEmhttp is the no-API-key fallback: shares.ini/sec.ini directly
// (HL-SA-22). Logged once per fallback at INFO, matching arrayFromEmhttp.
func (s *Source) sharesFromEmhttp(ctx context.Context) ([]core.Share, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.EmhttpTimeout())
	defer cancel()

	s.log.InfoContext(ctx, "unraid shares read from emhttp: unraid-api credential absent")
	shares, err := readSharesFromEmhttp(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "unraid shares emhttp fallback read failed", "error", err)
		return nil, fmt.Errorf("unraid shares: emhttp fallback: %w", err)
	}
	return shares, nil
}

func (s *Source) Containers(ctx context.Context) ([]core.Container, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.DockerTimeout())
	defer cancel()

	containers, err := s.docker.containers(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "unraid containers read failed", "error", err)
		return nil, fmt.Errorf("unraid containers: %w", err)
	}
	return containers, nil
}

func (s *Source) SmartFor(ctx context.Context, device string) (core.SmartAttrs, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.SmartTimeout())
	defer cancel()

	attrs, err := runSmartctl(ctx, s.cfg.SmartctlPath, device)
	if err != nil {
		if !isDiskStandby(err) {
			s.log.ErrorContext(ctx, "unraid smart read failed", "device", device, "error", err)
		}
		return core.SmartAttrs{}, err
	}
	return attrs, nil
}

func (s *Source) Reachability(ctx context.Context) (core.Reachability, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.ReachabilityTimeout())
	defer cancel()

	r, err := s.reach.Reachability(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "unraid reachability check failed", "error", err)
		return core.Reachability{}, fmt.Errorf("unraid reachability: %w", err)
	}
	return r, nil
}

// isDiskStandby reports whether err wraps core.ErrDiskStandby, so SmartFor's
// expected, frequent "disk asleep, skipped" outcome isn't logged as an
// error (GitHub #61: this is a normal outcome, not a failure).
func isDiskStandby(err error) bool {
	return err != nil && strings.Contains(err.Error(), core.ErrDiskStandby.Error())
}

// normalizeDevice ensures a bare device name ("sdc", as seen in emhttp's
// disks.ini) is returned as the "/dev/sdc" form smartctl and this source's
// own SmartFor expect (core.Disk.Device's doc comment: "the handle smartctl
// needs"). GraphQL's ArrayDisk.device was not observed live with real data
// (every real field needs the API key this ticket cannot create), so this
// normalizes defensively for either shape.
func normalizeDevice(device string) string {
	if device == "" || strings.HasPrefix(device, "/dev/") {
		return device
	}
	return "/dev/" + device
}

// kibToBytes converts Unraid's disk/share size fields, which are in
// kibibytes — confirmed by cross-checking a live disks.ini capture against
// its own reported drive capacities (e.g. a 6TB drive's `size`/`fsSize`
// fields are ~5.86e9, which is exactly the drive's byte capacity / 1024),
// not documented explicitly in the research doc's prose.
func kibToBytes(v bigInt) int64 {
	return int64(v) * 1024
}

// parseUptimeSeconds parses InfoOs.uptime. The GraphQL schema types it as a
// plain String (confirmed live via introspection) with no documented wire
// format and no authenticated call available to observe a real value (no
// API key exists yet). Rather than guess a single format and silently
// return a wrong number, this tries the formats a systeminformation-backed
// resolver (the library the rest of the Info type's shape matches) is known
// to emit, and fails loudly — per the interface contract, "returns an error
// rather than a zero value when the read fails" — if none match.
func parseUptimeSeconds(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("unraid host info: empty uptime value")
	}
	if v, err := strconv.ParseFloat(raw, 64); err == nil {
		return int64(v), nil
	}
	if d, err := parseHumanUptime(raw); err == nil {
		return int64(d.Seconds()), nil
	}
	return 0, fmt.Errorf("unraid host info: unrecognized uptime format %q", raw)
}

// parseHumanUptime handles a "Xd Yh Zm" / "Xd" / "Yh Zm" style duration
// string (Go's own %s duration is close but not identical: "1d2h3m" isn't
// valid time.ParseDuration syntax, which has no day unit).
func parseHumanUptime(raw string) (time.Duration, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty")
	}
	var total time.Duration
	matched := false
	for _, f := range fields {
		f = strings.TrimSuffix(f, ",")
		unit := f[len(f)-1:]
		numStr := f[:len(f)-1]
		var mult time.Duration
		switch unit {
		case "d":
			mult = 24 * time.Hour
		case "h":
			mult = time.Hour
		case "m":
			mult = time.Minute
		case "s":
			mult = time.Second
		default:
			continue
		}
		n, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			continue
		}
		total += time.Duration(n * float64(mult))
		matched = true
	}
	if !matched {
		return 0, fmt.Errorf("no recognizable duration fields in %q", raw)
	}
	return total, nil
}
