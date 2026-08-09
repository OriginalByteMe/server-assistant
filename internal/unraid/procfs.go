package unraid

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"server-assistant/internal/config"
	"server-assistant/internal/core"
)

// hostInfoFromProcfs is the no-API-key fallback for host CPU/memory/uptime
// (HL-SA-22): unraid-api's GraphQL is the only other source for these
// fields, and there is no INI equivalent the way Array/Shares have — the
// Unraid host's own procfs, bind-mounted read-only into this container, is
// the sole fallback source.
//
// cfg.HostProcPath is REQUIRED to point at the HOST's procfs
// (docker-compose.yml mounts it at `/host/proc`, never the container's own
// `/proc`, which describes the container itself — reporting it as host
// vitals would be exactly the fabrication CONVENTIONS rule 5 forbids). An
// absent or unreadable path is a hard error here; this function never falls
// back to the container's own /proc.
func hostInfoFromProcfs(ctx context.Context, cfg config.UnraidConfig) (core.HostInfo, error) {
	root := cfg.HostProcPath
	if root == "" {
		return core.HostInfo{}, errors.New("unraid procfs: host_proc_path not configured")
	}
	if err := ctx.Err(); err != nil {
		return core.HostInfo{}, err
	}

	total, used, err := readMemInfo(root)
	if err != nil {
		return core.HostInfo{}, fmt.Errorf("unraid procfs: %w", err)
	}
	uptime, err := readUptimeSeconds(root)
	if err != nil {
		return core.HostInfo{}, fmt.Errorf("unraid procfs: %w", err)
	}
	model, cores, err := readCPUInfo(root)
	if err != nil {
		return core.HostInfo{}, fmt.Errorf("unraid procfs: %w", err)
	}
	percent, err := readCPUPercent(ctx, root, cfg.CPUSampleInterval())
	if err != nil {
		return core.HostInfo{}, fmt.Errorf("unraid procfs: %w", err)
	}

	// Hostname/version come from var.ini via the existing emhttp reader when
	// it's present; if it isn't (no /var/local/emhttp mount, or the file is
	// missing), leave them empty rather than guessing (rule 5).
	var hostname, version string
	if vars, err := readVars(ctx); err == nil {
		hostname = vars["NAME"]
		version = vars["version"]
	}

	return core.HostInfo{
		Hostname:      hostname,
		UnraidVersion: version,
		CPUModel:      model,
		CPUCores:      cores,
		CPUPercent:    percent,
		MemTotalBytes: total,
		MemUsedBytes:  used,
		UptimeSeconds: uptime,
		Source:        core.SourceProcfs,
		CollectedAt:   time.Now(),
	}, nil
}

// readMemInfo parses <root>/meminfo. Used memory is MemTotal-MemAvailable,
// never MemTotal-MemFree: MemFree excludes the page cache, which every
// Unraid box carries a large amount of, so a MemFree-based figure wildly
// overstates "used" memory. MemAvailable is the kernel's own estimate of
// what's actually reclaimable/free for a new allocation, and matches what
// `free -b`'s "available" column (and thus a human's own sanity check)
// reports. Values in the file are KiB despite the "kB" label (a
// long-standing kernel labelling quirk), so the conversion is x1024, not
// x1000.
func readMemInfo(root string) (totalBytes, usedBytes int64, err error) {
	fields, err := readKVFile(filepath.Join(root, "meminfo"), ":")
	if err != nil {
		return 0, 0, err
	}
	total, ok := parseMemInfoKB(fields["MemTotal"])
	if !ok {
		return 0, 0, fmt.Errorf("meminfo: missing or unparseable MemTotal")
	}
	avail, ok := parseMemInfoKB(fields["MemAvailable"])
	if !ok {
		return 0, 0, fmt.Errorf("meminfo: missing or unparseable MemAvailable")
	}
	totalBytes = total * 1024
	usedBytes = (total - avail) * 1024
	return totalBytes, usedBytes, nil
}

// parseMemInfoKB extracts the leading integer from a meminfo value field
// ("32780028 kB" -> 32780028). ok is false when the field is absent or the
// leading token isn't a number.
func parseMemInfoKB(raw string) (int64, bool) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, false
	}
	return parseINIInt(fields[0])
}

// readUptimeSeconds parses <root>/uptime: a single line, "<uptime>
// <idle-sum>" in seconds, both possibly fractional. Only the first field is
// meaningful here.
func readUptimeSeconds(root string) (int64, error) {
	raw, err := os.ReadFile(filepath.Join(root, "uptime"))
	if err != nil {
		return 0, fmt.Errorf("uptime: %w", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, fmt.Errorf("uptime: empty file")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("uptime: parse %q: %w", fields[0], err)
	}
	return int64(seconds), nil
}

// readCPUInfo parses <root>/cpuinfo for the CPU model name (from the first
// "model name" line — every logical core repeats an identical one) and the
// logical core count (one "processor" line per logical core, hyperthreads
// included — the same count `nproc` reports and CPUPercent is scaled
// against).
func readCPUInfo(root string) (model string, cores int, err error) {
	f, err := os.Open(filepath.Join(root, "cpuinfo"))
	if err != nil {
		return "", 0, fmt.Errorf("cpuinfo: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, val, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "processor":
			cores++
		case "model name":
			if model == "" {
				model = val
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", 0, fmt.Errorf("cpuinfo: %w", err)
	}
	if cores == 0 {
		return "", 0, fmt.Errorf("cpuinfo: no processor lines found")
	}
	return model, cores, nil
}

// cpuStatSample is one /proc/stat aggregate "cpu " line's jiffy counters,
// split into idle (idle+iowait: time the CPU was NOT doing work, including
// waiting on I/O — the same convention top/mpstat use) and total (sum of
// every field).
type cpuStatSample struct {
	idle  uint64
	total uint64
}

// readCPUStatSample parses <root>/stat's aggregate "cpu  <user> <nice>
// <system> <idle> <iowait> <irq> <softirq> <steal> [<guest> <guest_nice>]"
// line. guest/guest_nice, present on newer kernels, are already counted
// inside user/nice by the kernel's own accounting, so summing every field
// present is correct either way.
func readCPUStatSample(root string) (cpuStatSample, error) {
	f, err := os.Open(filepath.Join(root, "stat"))
	if err != nil {
		return cpuStatSample{}, fmt.Errorf("stat: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var sample cpuStatSample
		for i, tok := range fields[1:] {
			v, err := strconv.ParseUint(tok, 10, 64)
			if err != nil {
				return cpuStatSample{}, fmt.Errorf("stat: parse cpu field %d (%q): %w", i, tok, err)
			}
			sample.total += v
			if i == 3 || i == 4 { // idle, iowait
				sample.idle += v
			}
		}
		return sample, nil
	}
	if err := scanner.Err(); err != nil {
		return cpuStatSample{}, fmt.Errorf("stat: %w", err)
	}
	return cpuStatSample{}, fmt.Errorf("stat: no aggregate cpu line found")
}

// cpuPercentFromSamples computes CPU utilization from two samples of the
// same counters, taken interval apart. A single /proc/stat read is
// cumulative jiffies since boot, not current load — this delta is the only
// way to get a "right now" figure. A zero or negative total delta (clock
// oddity, or the two samples landing on an unchanged counter) is an error,
// never a fabricated 0.0 (CONVENTIONS rule 5): 0% load is a real
// measurement, not the same thing as "couldn't tell".
func cpuPercentFromSamples(first, second cpuStatSample) (float64, error) {
	totalDelta := int64(second.total) - int64(first.total)
	if totalDelta <= 0 {
		return 0, fmt.Errorf("cpu percent: non-positive total jiffy delta (%d); cannot compute a real reading", totalDelta)
	}
	idleDelta := int64(second.idle) - int64(first.idle)
	return 100 * (1 - float64(idleDelta)/float64(totalDelta)), nil
}

// readCPUPercent takes two <root>/stat samples separated by interval,
// bounded by ctx, and returns the CPU utilization between them.
func readCPUPercent(ctx context.Context, root string, interval time.Duration) (float64, error) {
	first, err := readCPUStatSample(root)
	if err != nil {
		return 0, err
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-timer.C:
	}

	second, err := readCPUStatSample(root)
	if err != nil {
		return 0, err
	}
	return cpuPercentFromSamples(first, second)
}

// readKVFile parses a flat "<key><sep><value...>" file (meminfo's
// "MemTotal:       32780028 kB" shape) into a map keyed by the trimmed key.
// Unlike parseINI, there are no `[section]` headers here at all.
func readKVFile(path, sep string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	fields := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, val, ok := strings.Cut(scanner.Text(), sep)
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(val)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return fields, nil
}
