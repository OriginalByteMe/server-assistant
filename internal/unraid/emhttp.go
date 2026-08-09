package unraid

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"server-assistant/internal/core"
)

// emhttpDir is Unraid's own fixed OS path for its live-state INI files — not
// operator configuration (CONVENTIONS: no config knob for a value that never
// changes on the platform this binary targets). A package var, not a const,
// solely so tests can point it at a temp fixture directory.
var emhttpDir = "/var/local/emhttp"

// userSharesRoot is Unraid's fixed mount root for user shares. Same
// var-not-const reasoning as emhttpDir.
var userSharesRoot = "/mnt/user"

// parseINI parses Unraid's `["section"]` / `key="value"` emhttp format.
// bufio + strings only (no ini library — CONVENTIONS rule 3): the format has
// no nesting, no comments, and no line continuation in the files this source
// reads, confirmed by direct inspection of the real files
// (docs/research/unraid-state-sources.md, "Gaps and their direct sources").
func parseINI(r io.Reader) (map[string]map[string]string, error) {
	sections := map[string]map[string]string{}
	var current string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.Trim(line, `[]"`)
			sections[current] = map[string]string{}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || current == "" {
			continue // stray line outside any section: ignore rather than guess
		}
		sections[current][strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"`)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sections, nil
}

// readINI opens and parses one emhttp INI file. ctx is checked up front
// (rule 4): a canceled/expired context short-circuits before touching disk,
// even though a local file read on the host's own tmpfs has no useful way to
// be interrupted mid-read.
func readINI(ctx context.Context, name string) (map[string]map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(emhttpDir, name)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("unraid emhttp: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	sections, err := parseINI(f)
	if err != nil {
		return nil, fmt.Errorf("unraid emhttp: parse %s: %w", path, err)
	}
	return sections, nil
}

// shareGaps is the two fields GraphQL's Share type never exposes (confirmed
// via live introspection: Share has no pool-name or export-visibility
// field), keyed by share name.
type shareGaps struct {
	cachePool string
	exported  bool
}

// readShareGaps fills the CachePool/Exported gap in core.Share from
// shares.ini (cachePool field, confirmed present in a live capture) and
// sec.ini (export field: "-" means not exported, any other value — "e" was
// the only other value observed live — means exported).
func readShareGaps(ctx context.Context) (map[string]shareGaps, error) {
	shares, err := readINI(ctx, "shares.ini")
	if err != nil {
		return nil, err
	}
	sec, err := readINI(ctx, "sec.ini")
	if err != nil {
		return nil, err
	}
	gaps := make(map[string]shareGaps, len(shares))
	for name, fields := range shares {
		gaps[name] = shareGaps{cachePool: fields["cachePool"], exported: exportedFromSec(sec[name])}
	}
	return gaps, nil
}

// shareAccessible reports whether a share's mount directory currently stats
// successfully — a directly-observed signal (not an ini field, not a
// fabricated default) for whether the share is actually reachable right now,
// as opposed to merely configured.
func shareAccessible(name string) bool {
	_, err := os.Stat(filepath.Join(userSharesRoot, name))
	return err == nil
}

// exportedFromSec applies sec.ini's export-flag convention ("-" or empty
// means not exported; any other value — "e" was the only other value
// observed live — means exported) to one share's sec.ini fields. Shared by
// readShareGaps (GraphQL-path gap fill) and readSharesFromEmhttp (the
// no-API-key path), so both read the same signal the same way. A share with
// no sec.ini entry at all (nil map) reads every key as "", which is
// correctly not-exported — the same default readShareGaps relied on before
// this was factored out.
func exportedFromSec(fields map[string]string) bool {
	return fields["export"] != "-" && fields["export"] != ""
}

// parseINIInt parses one emhttp INI numeric field. ok is false for an empty
// or non-numeric value (e.g. disks.ini's temp="*" for a disk with no
// reading) — the caller's signal to leave the corresponding core field
// absent rather than substitute a zero (CONVENTIONS rule 5).
func parseINIInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// kibField reads one KiB-denominated INI field and converts it to bytes via
// the same kibToBytes conversion the GraphQL path uses (source.go). A
// missing or unparseable field becomes 0 bytes, not an error: every
// observed case (e.g. disks.ini's parity stanza has no fsUsed key at all,
// because parity carries no filesystem) is a genuine "not applicable" zero,
// the same zero GraphQL's own bigInt scalar defaults to on a null value.
func kibField(fields map[string]string, key string) int64 {
	n, _ := parseINIInt(fields[key])
	return kibToBytes(bigInt(n))
}

// readVars parses var.ini. Unlike every other file this package reads
// (disks.ini, shares.ini, sec.ini, ...), var.ini has no `["section"]`
// headers at all — confirmed by direct inspection of a live capture on
// rijkaardserver, every line is a bare key="value" pair for the whole file.
// parseINI's section-scoped parser would silently drop every line of it
// (its own "stray line outside any section: ignore rather than guess"
// rule), so this is a dedicated flat reader rather than a special case
// bolted onto parseINI's documented, tested section format.
func readVars(ctx context.Context) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(emhttpDir, "var.ini")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("unraid emhttp: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	fields := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(val), `"`)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("unraid emhttp: parse %s: %w", path, err)
	}
	return fields, nil
}

// diskFromEmhttp converts one disks.ini stanza to core.Disk. ok is false for
// an unpopulated array slot (device=="", status="DISK_NP*" in the real
// capture) — there is no disk to report, and reporting one with a
// fabricated zero size would claim hardware that isn't there.
func diskFromEmhttp(name string, fields map[string]string) (core.Disk, bool) {
	device := fields["device"]
	if device == "" {
		return core.Disk{}, false
	}

	var tempC *int
	if t, ok := parseINIInt(fields["temp"]); ok { // "*" (no reading) fails to parse and stays nil
		v := int(t)
		tempC = &v
	}

	// disks.ini's own coarse status ("DISK_OK" vs everything else) is the
	// closest signal this file carries to GraphQL's smartStatus verdict —
	// confirmed live, every present array disk on rijkaardserver reports
	// DISK_OK. Anything else collapses to the same "UNKNOWN" sentinel
	// mapArrayDisks already uses when GraphQL itself has no verdict
	// (source.go), rather than inventing a third vocabulary.
	smartStatus := "UNKNOWN"
	if fields["status"] == "DISK_OK" {
		smartStatus = "OK"
	}

	return core.Disk{
		Name:        name,
		Device:      normalizeDevice(device),
		Role:        strings.ToLower(fields["type"]),
		SizeBytes:   kibField(fields, "size"),
		UsedBytes:   kibField(fields, "fsUsed"), // 0 for parity: no filesystem, no fsUsed key at all
		TempC:       tempC,
		SmartStatus: smartStatus,
		SpunDown:    fields["spundown"] == "1",
	}, true
}

// readArrayStateFromEmhttp builds core.ArrayState from var.ini (array/parity
// state) and disks.ini (the disk list) — the no-API-key fallback path
// (source.go's Array()). Field-by-field provenance, all confirmed against a
// live capture on rijkaardserver and cross-checked against
// /usr/local/emhttp/plugins/dynamix/scripts/statuscheck — the same
// decompiled-source technique docs/research/unraid-state-sources.md uses
// throughout:
//
//   - State <- mdState, passed through like GraphQL's Array.state.
//   - ParityCheckActive <- mdResync != 0. mdResync is itself the *current
//     operation's* total size in KiB (0 when idle), not a boolean — that's
//     the vendor's own gate, read directly from statuscheck's
//     `if ($mdResync>0)` branch.
//   - ParityCheckProgress, only computed while active, is statuscheck's own
//     formula (`$mdResyncPos/($mdResync/100+1)`, in percent) — not
//     independently verified live because forcing a parity check to
//     reproduce it is a host mutation this ticket may not perform; the
//     formula itself is read straight off the vendor's shipped script, the
//     same confidence level the research doc uses for decompiled sources.
//   - ParityLastCheck <- sbSynced2 (the completion timestamp statuscheck
//     displays as "Last checked on", not sbSynced, which is the *start*
//     time of the same operation — confirmed live, the two values differ by
//     exactly the last check's duration). 0 means never checked and stays
//     nil.
//   - ParityLastErrors <- sbSyncErrs, unconditionally (statuscheck shows it
//     in both the "corrected" and "detected" cases).
func readArrayStateFromEmhttp(ctx context.Context) (core.ArrayState, error) {
	vars, err := readVars(ctx)
	if err != nil {
		return core.ArrayState{}, err
	}
	diskSections, err := readINI(ctx, "disks.ini")
	if err != nil {
		return core.ArrayState{}, err
	}

	var disks []core.Disk
	for name, fields := range diskSections {
		if name == "flash" {
			continue // boot device: outside GraphQL's array.{disks,caches,parities} scope too
		}
		if d, ok := diskFromEmhttp(name, fields); ok {
			disks = append(disks, d)
		}
	}

	active := false
	if n, ok := parseINIInt(vars["mdResync"]); ok {
		active = n != 0
	}
	var progress float64
	if active {
		total, _ := parseINIInt(vars["mdResync"])
		pos, _ := parseINIInt(vars["mdResyncPos"])
		progress = float64(pos) / (float64(total)/100 + 1)
	}

	var lastCheck *time.Time
	if n, ok := parseINIInt(vars["sbSynced2"]); ok && n > 0 {
		t := time.Unix(n, 0).UTC()
		lastCheck = &t
	}
	var lastErrors int64
	if n, ok := parseINIInt(vars["sbSyncErrs"]); ok {
		lastErrors = n
	}

	return core.ArrayState{
		State:               vars["mdState"],
		ParityCheckActive:   active,
		ParityCheckProgress: progress,
		ParityLastCheck:     lastCheck,
		ParityLastErrors:    lastErrors,
		Source:              core.SourceEmhttp,
		Disks:               disks,
		CollectedAt:         time.Now(),
	}, nil
}

// readSharesFromEmhttp builds the full core.Share list straight from
// shares.ini and sec.ini — the no-API-key fallback path (source.go's
// Shares()). Unlike readShareGaps (which only fills the two fields GraphQL
// itself never exposes), this is the sole source here, so every field
// core.Share documents gets populated straight from the INI.
func readSharesFromEmhttp(ctx context.Context) ([]core.Share, error) {
	shares, err := readINI(ctx, "shares.ini")
	if err != nil {
		return nil, err
	}
	sec, err := readINI(ctx, "sec.ini")
	if err != nil {
		return nil, err
	}

	out := make([]core.Share, 0, len(shares))
	for name, fields := range shares {
		out = append(out, core.Share{
			Name:       name,
			SizeBytes:  kibField(fields, "size"),
			FreeBytes:  kibField(fields, "free"),
			UsedBytes:  kibField(fields, "used"),
			Allocator:  fields["allocator"],
			CachePool:  fields["cachePool"],
			Exported:   exportedFromSec(sec[name]),
			Accessible: shareAccessible(name),
			Source:     core.SourceEmhttp,
		})
	}
	return out, nil
}
