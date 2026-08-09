// unraid_api.go — JSON mirror of the /unraid dashboard's live Unraid state
// (unraid.go), the same relationship api.go has to incidents.go. DTOs are
// declared locally rather than tagging core types, so the wire format never
// leaks into the domain model (api.go's convention). Nullable fields (e.g.
// a spun-down disk's TempC) stay nullable — CONVENTIONS rule 5 forbids a
// zero standing in for an unread value, in JSON exactly as much as in HTML.
package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"server-assistant/internal/core"
)

func registerUnraidAPIRoutes(mux *http.ServeMux, us core.UnraidSource) {
	mux.HandleFunc("GET /api/unraid/host", handleAPIUnraidHost(us))
	mux.HandleFunc("GET /api/unraid/array", handleAPIUnraidArray(us))
	mux.HandleFunc("GET /api/unraid/shares", handleAPIUnraidShares(us))
	mux.HandleFunc("GET /api/unraid/containers", handleAPIUnraidContainers(us))
}

// unraidErrorDTO is the JSON error envelope every /api/unraid/* route
// returns on failure, so a script hitting the API gets the same honest
// diagnosis as a human on the page — never a 200 with an empty/zeroed body.
type unraidErrorDTO struct {
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

func writeUnraidError(w http.ResponseWriter, err error) {
	if errors.Is(err, core.ErrUnauthenticated) {
		writeJSON(w, http.StatusUnauthorized, unraidErrorDTO{Error: "unauthenticated", Detail: unauthHint})
		return
	}
	writeJSON(w, http.StatusBadGateway, unraidErrorDTO{Error: "unreachable", Detail: err.Error()})
}

func handleAPIUnraidHost(us core.UnraidSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		h, err := us.HostInfo(ctx)
		if err != nil {
			writeUnraidError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toHostDTO(h))
	}
}

func handleAPIUnraidArray(us core.UnraidSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		a, err := us.Array(ctx)
		if err != nil {
			writeUnraidError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, toArrayDTO(a))
	}
}

func handleAPIUnraidShares(us core.UnraidSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		shares, err := us.Shares(ctx)
		if err != nil {
			writeUnraidError(w, err)
			return
		}
		dtos := make([]shareDTO, 0, len(shares))
		for _, s := range shares {
			dtos = append(dtos, toShareDTO(s))
		}
		writeJSON(w, http.StatusOK, dtos)
	}
}

func handleAPIUnraidContainers(us core.UnraidSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		containers, err := us.Containers(ctx)
		if err != nil {
			writeUnraidError(w, err)
			return
		}
		dtos := make([]containerDTO, 0, len(containers))
		for _, c := range containers {
			dtos = append(dtos, toContainerDTO(c))
		}
		writeJSON(w, http.StatusOK, dtos)
	}
}

type hostDTO struct {
	Hostname      string  `json:"hostname"`
	UnraidVersion string  `json:"unraid_version"`
	CPUModel      string  `json:"cpu_model"`
	CPUCores      int     `json:"cpu_cores"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemTotalBytes int64   `json:"mem_total_bytes"`
	MemUsedBytes  int64   `json:"mem_used_bytes"`
	UptimeSeconds int64   `json:"uptime_seconds"`
	// Source is "unraid-api", "procfs" or "emhttp" — which path produced
	// this reading. Without it a consumer cannot tell full API fidelity from
	// the key-free fallback.
	Source      string `json:"source"`
	CollectedAt string `json:"collected_at"`
}

func toHostDTO(h core.HostInfo) hostDTO {
	return hostDTO{
		Hostname:      h.Hostname,
		UnraidVersion: h.UnraidVersion,
		CPUModel:      h.CPUModel,
		CPUCores:      h.CPUCores,
		CPUPercent:    h.CPUPercent,
		MemTotalBytes: h.MemTotalBytes,
		MemUsedBytes:  h.MemUsedBytes,
		UptimeSeconds: h.UptimeSeconds,
		Source:        string(h.Source),
		CollectedAt:   h.CollectedAt.Format(time.RFC3339),
	}
}

type diskDTO struct {
	Name      string `json:"name"`
	Device    string `json:"device"`
	Role      string `json:"role"`
	SizeBytes int64  `json:"size_bytes"`
	UsedBytes int64  `json:"used_bytes"`
	// TempC is JSON null for a spun-down (or otherwise unread) disk — never
	// a fake 0.
	TempC       *int   `json:"temp_c"`
	SmartStatus string `json:"smart_status"`
	SpunDown    bool   `json:"spun_down"`
}

func toDiskDTO(d core.Disk) diskDTO {
	return diskDTO{
		Name:        d.Name,
		Device:      d.Device,
		Role:        d.Role,
		SizeBytes:   d.SizeBytes,
		UsedBytes:   d.UsedBytes,
		TempC:       d.TempC,
		SmartStatus: d.SmartStatus,
		SpunDown:    d.SpunDown,
	}
}

type arrayDTO struct {
	State               string    `json:"state"`
	ParityCheckActive   bool      `json:"parity_check_active"`
	ParityCheckProgress float64   `json:"parity_check_progress"`
	ParityLastCheck     *string   `json:"parity_last_check"`
	ParityLastErrors    int64     `json:"parity_last_errors"`
	Disks               []diskDTO `json:"disks"`
	// Source is where this reading came from: "unraid-api" (full fidelity)
	// or "emhttp" (the key-free INI fallback, which carries fewer fields).
	// Without it a consumer cannot tell an absent field from a degraded read.
	Source      string `json:"source"`
	CollectedAt string `json:"collected_at"`
}

func toArrayDTO(a core.ArrayState) arrayDTO {
	disks := make([]diskDTO, 0, len(a.Disks))
	for _, d := range a.Disks {
		disks = append(disks, toDiskDTO(d))
	}
	var lastCheck *string
	if a.ParityLastCheck != nil {
		lastCheck = rfc3339Ptr(*a.ParityLastCheck)
	}
	return arrayDTO{
		State:               a.State,
		ParityCheckActive:   a.ParityCheckActive,
		ParityCheckProgress: a.ParityCheckProgress,
		ParityLastCheck:     lastCheck,
		ParityLastErrors:    a.ParityLastErrors,
		Disks:               disks,
		Source:              string(a.Source),
		CollectedAt:         a.CollectedAt.Format(time.RFC3339),
	}
}

type shareDTO struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"size_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
	UsedBytes  int64  `json:"used_bytes"`
	Allocator  string `json:"allocator"`
	CachePool  string `json:"cache_pool"`
	Exported   bool   `json:"exported"`
	Accessible bool   `json:"accessible"`
	Source     string `json:"source"`
}

func toShareDTO(s core.Share) shareDTO {
	return shareDTO{
		Name:       s.Name,
		SizeBytes:  s.SizeBytes,
		FreeBytes:  s.FreeBytes,
		UsedBytes:  s.UsedBytes,
		Allocator:  s.Allocator,
		CachePool:  s.CachePool,
		Exported:   s.Exported,
		Accessible: s.Accessible,
		Source:     string(s.Source),
	}
}

type containerDTO struct {
	Name    string   `json:"name"`
	Image   string   `json:"image"`
	State   string   `json:"state"`
	Status  string   `json:"status"`
	Ports   []string `json:"ports"`
	AutoRun bool     `json:"auto_run"`
}

func toContainerDTO(c core.Container) containerDTO {
	return containerDTO{
		Name:    c.Name,
		Image:   c.Image,
		State:   c.State,
		Status:  c.Status,
		Ports:   c.Ports,
		AutoRun: c.AutoRun,
	}
}
