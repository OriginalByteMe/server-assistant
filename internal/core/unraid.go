package core

import (
	"context"
	"errors"
	"time"
)

// The Unraid state surface (GitHub #53, #57). Server Assistant runs on the
// Unraid Host and aggregates its state for two consumers: the dashboard and
// the user's own LLM over MCP. It contains no inference of its own.
//
// Two sources sit behind the one seam, because the split is a fact about
// Unraid rather than a design choice (docs/research/unraid-state-sources.md):
//
//   - unraid-api's GraphQL exposes array, shares, containers, host info and a
//     coarse smartStatus, and needs an API key.
//   - Raw SMART attributes are parsed by unraid-api but never reach the
//     schema, so they are read directly with `smartctl -n standby -A -j` —
//     the same invocation the vendor's own resolvers use.
//
// CONVENTIONS rule 5 (the observer never lies) governs every field here: a
// value that could not be read is absent or an error, never a zero standing
// in for a real measurement.

// ErrDiskStandby reports that a disk was spun down and was deliberately not
// woken. It is a normal outcome, not a failure: the sampler skips the disk and
// accepts the gap rather than keeping the array awake (GitHub #61).
var ErrDiskStandby = errors.New("disk in standby; not woken")

// ErrUnauthenticated reports that the Unraid API rejected the credential.
// Surfaced verbatim to the dashboard so a missing API key is diagnosable
// rather than looking like an empty machine.
var ErrUnauthenticated = errors.New("unraid api credential rejected")

// HostInfo is the machine itself.
type HostInfo struct {
	Hostname      string
	UnraidVersion string
	CPUModel      string
	CPUCores      int
	CPUPercent    float64
	MemTotalBytes int64
	MemUsedBytes  int64
	UptimeSeconds int64
	CollectedAt   time.Time
}

// StateSource names where a reading actually came from. It exists because
// this product degrades rather than fails when no unraid-api credential is
// available: array and share state fall back to /var/local/emhttp's INI
// files, which carry fewer fields than GraphQL does. A user comparing a
// sparse dashboard against a full one must be able to tell "this machine has
// no parity data" from "we read this the cheap way" — collapsing those two
// into an indistinguishable view is exactly the lie CONVENTIONS rule 5
// forbids.
type StateSource string

const (
	// SourceUnraidAPI — read from unraid-api's GraphQL, the full-fidelity path.
	SourceUnraidAPI StateSource = "unraid-api"
	// SourceEmhttp — read from /var/local/emhttp/*.ini because no API
	// credential was available. Fewer fields; absent ones stay absent.
	SourceEmhttp StateSource = "emhttp"
)

// ArrayState is the array and its parity, the noun an Unraid user thinks in.
type ArrayState struct {
	// State is Unraid's own vocabulary: STARTED, STOPPED, NEW_ARRAY, ...
	// Passed through rather than remapped, so the LLM and the Unraid UI agree.
	State string
	// ParityCheckActive and Progress describe a running check; Progress is
	// only meaningful while Active.
	ParityCheckActive   bool
	ParityCheckProgress float64
	ParityLastCheck     *time.Time
	ParityLastErrors    int64
	// Source records which path produced this reading. Never leave it empty
	// on a successful read.
	Source      StateSource
	Disks       []Disk
	CollectedAt time.Time
}

// Disk is one physical device as the array sees it.
type Disk struct {
	Name   string // Unraid's slot name: disk1, parity, cache
	Device string // /dev/sdX, the handle smartctl needs
	// Role is data, parity or cache; drives how the dashboard groups it.
	Role      string
	SizeBytes int64
	UsedBytes int64
	// TempC is nil when the disk is spun down or the reading was unavailable.
	TempC *int
	// SmartStatus is unraid-api's coarse verdict (OK / UNKNOWN), not a
	// substitute for SmartAttrs.
	SmartStatus string
	SpunDown    bool
}

// Share is a user share.
type Share struct {
	Name       string
	SizeBytes  int64
	FreeBytes  int64
	UsedBytes  int64
	Allocator  string
	CachePool  string
	Exported   bool
	Accessible bool
	// Source records which path produced this reading. Never leave it empty
	// on a successful read.
	Source StateSource
}

// Container is one Docker container on the Host.
type Container struct {
	Name    string
	Image   string
	State   string // running, exited, ...
	Status  string // human string: "Up 16 days"
	Ports   []string
	AutoRun bool
}

// SmartAttrs is the raw attribute table for one device — the series where
// history is the signal (GitHub #61). "Reallocated sectors: 8" means nothing
// alone and a great deal next to "was 0 last week".
type SmartAttrs struct {
	Device      string
	ModelName   string
	SerialHash  string // hashed, never the raw serial: it is identifying
	Attributes  []SmartAttr
	CollectedAt time.Time
}

// SmartAttr is one SMART attribute row.
type SmartAttr struct {
	ID        int
	Name      string
	Value     int
	Worst     int
	Threshold int
	RawValue  int64
}

// UnraidSource reads Host state. Every method takes a context with an explicit
// timeout (CONVENTIONS rule 4) and returns an error rather than a zero value
// when the read fails (rule 5).
//
// Implementations must be safe for concurrent use: the dashboard, the MCP
// endpoint and the sampler all read through one instance.
type UnraidSource interface {
	HostInfo(ctx context.Context) (HostInfo, error)
	Array(ctx context.Context) (ArrayState, error)
	Shares(ctx context.Context) ([]Share, error)
	Containers(ctx context.Context) ([]Container, error)
	// SmartFor returns ErrDiskStandby for a spun-down disk and never wakes it.
	SmartFor(ctx context.Context, device string) (SmartAttrs, error)
	// Reachability reports how this process can currently be reached, so the
	// dashboard can tell "tailnet-only" from "publicly served" from "broken"
	// instead of failing silently (GitHub #51, #56).
	Reachability(ctx context.Context) (Reachability, error)
}

// ReachState is the four-way answer the dashboard self-check must distinguish.
type ReachState string

const (
	// ReachAbsent — no Tailscale on the Host at all.
	ReachAbsent ReachState = "absent"
	// ReachTailnet — reachable on the tailnet only. A cloud LLM cannot
	// connect; this is the state that otherwise fails silently.
	ReachTailnet ReachState = "tailnet"
	// ReachFunnel — served publicly over Tailscale Funnel.
	ReachFunnel ReachState = "funnel"
	// ReachFailing — an endpoint is configured but does not answer.
	ReachFailing ReachState = "failing"
)

// Reachability is the self-check result.
type Reachability struct {
	State ReachState
	// PublicURL is set only in ReachFunnel.
	PublicURL string
	// TailnetURL is the tailnet-scoped address, when Tailscale is present.
	TailnetURL string
	// Detail explains the state in one sentence for a non-expert.
	Detail      string
	CollectedAt time.Time
}
