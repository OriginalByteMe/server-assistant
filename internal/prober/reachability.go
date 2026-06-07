package prober

import "time"

// NewReachability returns the Host reachability Probe (ADR 0005). Host
// reachability is exactly a TCP dial pointed at the Host instead of a
// Service, so it is a thin semantic alias over the canonical TCP prober —
// the dial/cancel/latency/timeout logic lives in exactly one place (tcp.go).
// Behaviour is unchanged from ARK-12; the ARK-12 reachability tests guard it.
func NewReachability(name, address string, timeout time.Duration) *TCP {
	return NewTCP(name, address, timeout)
}
