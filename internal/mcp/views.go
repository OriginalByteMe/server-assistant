package mcp

import "server-assistant/internal/core"

// View builders shared between tools (B2 summary/detail split) and
// resources (always the full view — a resource has no detail knob). Maps
// with camelCase keys are built explicitly rather than adding json tags to
// core types, matching the rest of the codebase's DTO convention
// (internal/web/api.go: "DTOs are declared locally rather than adding json
// tags to core types, so the wire format never leaks into the domain
// model").

// Every view that has provenance emits "source" unconditionally — NOT
// behind detail. The LLM is this surface's primary consumer and it cannot
// weigh a reading it cannot attribute: "unraid-api" is full fidelity,
// "emhttp" and "procfs" are the degraded fallbacks. internal/web already
// surfaces this on every DTO; the MCP views used to drop it, so a model
// asking get_host_info got a number with no way to tell which path
// produced it (found live via a real Claude MCP session, 2026-08-09).
func hostInfoView(hi core.HostInfo, detail bool) map[string]any {
	v := map[string]any{
		"hostname":      hi.Hostname,
		"unraidVersion": hi.UnraidVersion,
		"cpuPercent":    hi.CPUPercent,
		"uptimeHours":   float64(hi.UptimeSeconds) / 3600,
		"source":        string(hi.Source),
	}
	if detail {
		v["cpuModel"] = hi.CPUModel
		v["cpuCores"] = hi.CPUCores
		v["memTotalBytes"] = hi.MemTotalBytes
		v["memUsedBytes"] = hi.MemUsedBytes
		v["collectedAt"] = hi.CollectedAt
	}
	return v
}

func reachabilityView(r core.Reachability, detail bool) map[string]any {
	v := map[string]any{
		"state":  r.State,
		"detail": r.Detail,
	}
	if detail {
		v["publicUrl"] = r.PublicURL
		v["tailnetUrl"] = r.TailnetURL
		v["collectedAt"] = r.CollectedAt
	}
	return v
}

func diskView(d core.Disk, detail bool) map[string]any {
	v := map[string]any{
		"name":        d.Name,
		"device":      d.Device,
		"role":        d.Role,
		"tempC":       d.TempC,
		"smartStatus": d.SmartStatus,
		"spunDown":    d.SpunDown,
	}
	if detail {
		v["sizeBytes"] = d.SizeBytes
		v["usedBytes"] = d.UsedBytes
	}
	return v
}

func arrayView(a core.ArrayState, detail bool) map[string]any {
	disks := make([]map[string]any, len(a.Disks))
	for i, d := range a.Disks {
		disks[i] = diskView(d, detail)
	}
	v := map[string]any{
		"state":               a.State,
		"parityCheckActive":   a.ParityCheckActive,
		"parityCheckProgress": a.ParityCheckProgress,
		"disks":               disks,
		"source":              string(a.Source),
	}
	if detail {
		v["parityLastCheck"] = a.ParityLastCheck
		v["parityLastErrors"] = a.ParityLastErrors
		v["collectedAt"] = a.CollectedAt
	}
	return v
}

func smartAttrView(a core.SmartAttr) map[string]any {
	return map[string]any{
		"id":        a.ID,
		"name":      a.Name,
		"value":     a.Value,
		"worst":     a.Worst,
		"threshold": a.Threshold,
		"rawValue":  a.RawValue,
	}
}

func smartView(sm core.SmartAttrs, detail bool) map[string]any {
	attrs := sm.Attributes
	if !detail {
		attrs = curatedSmartAttrs(attrs)
	}
	rows := make([]map[string]any, len(attrs))
	for i, a := range attrs {
		rows[i] = smartAttrView(a)
	}
	return map[string]any{
		"device":             sm.Device,
		"model":              sm.ModelName,
		"collectedAt":        sm.CollectedAt,
		"temperatureCelsius": sm.TemperatureCelsius,
		"attributes":         rows,
	}
}

func shareView(sh core.Share, detail bool) map[string]any {
	v := map[string]any{
		"name":      sh.Name,
		"usedBytes": sh.UsedBytes,
		"freeBytes": sh.FreeBytes,
	}
	if detail {
		v["sizeBytes"] = sh.SizeBytes
		v["allocator"] = sh.Allocator
		v["cachePool"] = sh.CachePool
		v["exported"] = sh.Exported
		v["accessible"] = sh.Accessible
	}
	return v
}

// sharesSource is the provenance for a whole list_shares reading. Emitted
// once on the envelope rather than repeated on all ~24 shares: Shares() is
// a single collector call, so the value is uniform in practice, and this
// surface is token-budgeted (budget.go). "mixed" is returned rather than
// picking a winner if that assumption ever breaks — a per-share attribution
// is recoverable from internal/web's DTO, but a silently wrong single
// source would be exactly the lie CONVENTIONS rule 5 forbids.
func sharesSource(shares []core.Share) string {
	if len(shares) == 0 {
		return ""
	}
	first := string(shares[0].Source)
	for _, sh := range shares[1:] {
		if string(sh.Source) != first {
			return "mixed"
		}
	}
	return first
}

func sharesView(shares []core.Share, detail bool) []map[string]any {
	out := make([]map[string]any, len(shares))
	for i, sh := range shares {
		out[i] = shareView(sh, detail)
	}
	return out
}

func containerView(c core.Container, detail bool) map[string]any {
	v := map[string]any{
		"name":    c.Name,
		"state":   c.State,
		"autoRun": c.AutoRun,
	}
	if detail {
		v["image"] = c.Image
		v["status"] = c.Status
		v["ports"] = c.Ports
	}
	return v
}

func containersView(containers []core.Container, detail bool) []map[string]any {
	out := make([]map[string]any, len(containers))
	for i, c := range containers {
		out[i] = containerView(c, detail)
	}
	return out
}

// curatedSmartIDs are the standard SMART attribute IDs most predictive of
// impending failure (reallocated/pending sectors, uncorrectable errors,
// CRC errors, power-on hours) — the subset a summary keeps when detail is
// not requested. History across samples is the real signal (GitHub #61),
// but a single-call summary still needs to stay small.
var curatedSmartIDs = map[int]bool{
	5:   true, // Reallocated_Sector_Ct
	9:   true, // Power_On_Hours
	187: true, // Reported_Uncorrect
	188: true, // Command_Timeout
	196: true, // Reallocated_Event_Count
	197: true, // Current_Pending_Sector
	198: true, // Offline_Uncorrectable
	199: true, // UDMA_CRC_Error_Count
}

func curatedSmartAttrs(attrs []core.SmartAttr) []core.SmartAttr {
	out := make([]core.SmartAttr, 0, len(curatedSmartIDs))
	for _, a := range attrs {
		if curatedSmartIDs[a.ID] {
			out = append(out, a)
		}
	}
	return out
}
