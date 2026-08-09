package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/core"
)

// TestStore_HarnessCycleRoundTrip: a full Harness cycle with tool calls,
// Diagnosis (including Usage), and non-zero timestamps round-trips through
// Save and Get unchanged.
func TestStore_HarnessCycleRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "harness.db")
	at := time.Date(2026, 5, 17, 12, 0, 0, 500_000_000, time.UTC)

	s1, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s1.Migrate(ctx))

	cycle := core.HarnessCycle{
		ID:            "cycle-1",
		Subject:       "web",
		TriggerStatus: core.StatusDown,
		Mode:          core.HarnessLive,
		StartedAt:     at,
		ToolCalls: []core.ToolCall{
			{
				Tool:     "container_status",
				Args:     map[string]string{"service": "web"},
				Output:   "DOWN",
				Err:      "",
				At:       at.Add(100 * time.Millisecond),
				Duration: 50 * time.Millisecond,
			},
			{
				Tool:     "container_logs",
				Args:     map[string]string{"service": "web", "lines": "50"},
				Output:   "error in log",
				Err:      "",
				At:       at.Add(200 * time.Millisecond),
				Duration: 150 * time.Millisecond,
			},
		},
		Diagnosis: core.Diagnosis{
			Summary: "Container crashed",
			Proposed: core.ProposedAction{
				Kind:      core.ActionRestartContainer,
				Subject:   "web",
				Rationale: "Container was down, restart should restore service",
			},
			Usage: core.Usage{
				Backend:          "ollama",
				Model:            "qwen2.5:1.5b-instruct",
				PromptTokens:     200,
				CompletionTokens: 50,
				Latency:          500 * time.Millisecond,
			},
			Fallback: false,
		},
		Approval:       core.ApprovalApproved,
		ApprovedBy:     "operator",
		ApprovedAt:     at.Add(1 * time.Second),
		ResolvedTarget: "sa-demo-web",
		DispatchResult: "SSH exec succeeded",
		DispatchedAt:   at.Add(2 * time.Second),
		Outcome:        core.OutcomeRecovered,
		OutcomeAt:      at.Add(3 * time.Second),
		Error:          "",
	}

	require.NoError(t, s1.SaveHarnessCycle(ctx, cycle))
	require.NoError(t, s1.Close())

	// Reopen and verify exact equality.
	s2, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s2.Migrate(ctx))
	defer func() { require.NoError(t, s2.Close()) }()

	got, err := s2.GetHarnessCycle(ctx, "cycle-1")
	require.NoError(t, err)

	require.Equal(t, cycle.ID, got.ID)
	require.Equal(t, cycle.Subject, got.Subject)
	require.Equal(t, cycle.TriggerStatus, got.TriggerStatus)
	require.Equal(t, cycle.Mode, got.Mode)
	require.True(t, cycle.StartedAt.Equal(got.StartedAt), "StartedAt: want %s got %s", cycle.StartedAt, got.StartedAt)
	require.Len(t, got.ToolCalls, 2)
	require.Equal(t, cycle.ToolCalls[0].Tool, got.ToolCalls[0].Tool)
	require.Equal(t, cycle.ToolCalls[0].Args, got.ToolCalls[0].Args)
	require.Equal(t, cycle.ToolCalls[0].Output, got.ToolCalls[0].Output)
	require.True(t, cycle.ToolCalls[0].At.Equal(got.ToolCalls[0].At))
	require.Equal(t, cycle.ToolCalls[0].Duration, got.ToolCalls[0].Duration)

	require.Equal(t, cycle.Diagnosis.Summary, got.Diagnosis.Summary)
	require.Equal(t, cycle.Diagnosis.Proposed, got.Diagnosis.Proposed)
	require.Equal(t, cycle.Diagnosis.Usage.Backend, got.Diagnosis.Usage.Backend)
	require.Equal(t, cycle.Diagnosis.Usage.PromptTokens, got.Diagnosis.Usage.PromptTokens)
	require.Equal(t, cycle.Diagnosis.Usage.Latency, got.Diagnosis.Usage.Latency)

	require.Equal(t, cycle.Approval, got.Approval)
	require.Equal(t, cycle.ApprovedBy, got.ApprovedBy)
	require.True(t, cycle.ApprovedAt.Equal(got.ApprovedAt))
	require.Equal(t, cycle.Outcome, got.Outcome)
	require.True(t, cycle.OutcomeAt.Equal(got.OutcomeAt))
}

// TestStore_HarnessCycleZeroTimesRoundTrip: zero time.Time values round-trip
// as IsZero() == true, not as unix epoch 0.
func TestStore_HarnessCycleZeroTimesRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "zero_times.db")
	at := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	s1, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s1.Migrate(ctx))

	cycle := core.HarnessCycle{
		ID:            "cycle-zero",
		Subject:       "web",
		TriggerStatus: core.StatusDown,
		Mode:          core.HarnessShadow,
		StartedAt:     at,
		ToolCalls:     []core.ToolCall{},
		Diagnosis: core.Diagnosis{
			Summary: "pending",
			Proposed: core.ProposedAction{
				Kind: core.ActionNone,
			},
			Usage: core.Usage{},
		},
		Approval:       core.ApprovalPending,
		ApprovedBy:     "",
		ApprovedAt:     time.Time{}, // zero
		ResolvedTarget: "",
		DispatchResult: "",
		DispatchedAt:   time.Time{}, // zero
		Outcome:        core.OutcomeNone,
		OutcomeAt:      time.Time{}, // zero
		Error:          "",
	}

	require.NoError(t, s1.SaveHarnessCycle(ctx, cycle))
	require.NoError(t, s1.Close())

	s2, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s2.Migrate(ctx))
	defer func() { require.NoError(t, s2.Close()) }()

	got, err := s2.GetHarnessCycle(ctx, "cycle-zero")
	require.NoError(t, err)

	require.True(t, got.ApprovedAt.IsZero(), "ApprovedAt should be zero")
	require.True(t, got.DispatchedAt.IsZero(), "DispatchedAt should be zero")
	require.True(t, got.OutcomeAt.IsZero(), "OutcomeAt should be zero")
}

// TestStore_HarnessCycleUpsertProgression: save a cycle as pending, then
// update the same ID with approval/dispatch/outcome, and verify Get reflects
// the latest state. List still returns exactly 1 row (upsert, not append).
func TestStore_HarnessCycleUpsertProgression(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upsert_progression.db")
	at := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	s, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s.Migrate(ctx))
	defer func() { require.NoError(t, s.Close()) }()

	// Initial save: pending state.
	cycle := core.HarnessCycle{
		ID:            "progression-1",
		Subject:       "web",
		TriggerStatus: core.StatusDown,
		Mode:          core.HarnessLive,
		StartedAt:     at,
		ToolCalls:     []core.ToolCall{},
		Diagnosis: core.Diagnosis{
			Summary:  "pending",
			Proposed: core.ProposedAction{Kind: core.ActionNone},
		},
		Approval:   core.ApprovalPending,
		ApprovedBy: "",
		OutcomeAt:  time.Time{},
		Outcome:    core.OutcomePending,
	}
	require.NoError(t, s.SaveHarnessCycle(ctx, cycle))

	// Update: approval granted.
	cycle.Approval = core.ApprovalApproved
	cycle.ApprovedBy = "operator"
	cycle.ApprovedAt = at.Add(time.Minute)
	require.NoError(t, s.SaveHarnessCycle(ctx, cycle))

	// Verify Get reflects the latest (approved) state.
	got, err := s.GetHarnessCycle(ctx, "progression-1")
	require.NoError(t, err)
	require.Equal(t, core.ApprovalApproved, got.Approval)
	require.Equal(t, "operator", got.ApprovedBy)

	// Verify List still returns exactly 1 row (upsert, not append).
	all, err := s.ListHarnessCycles(ctx, 100)
	require.NoError(t, err)
	require.Len(t, all, 1)
}

// TestStore_ListHarnessCyclesOrdering: cycles are returned newest-first
// (ORDER BY started_at DESC) and respect the limit.
func TestStore_ListHarnessCyclesOrdering(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ordering.db")
	base := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	s, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s.Migrate(ctx))
	defer func() { require.NoError(t, s.Close()) }()

	// Save 3 cycles at different times.
	for i := 0; i < 3; i++ {
		cycle := core.HarnessCycle{
			ID:            "cycle-" + string(rune('A'+i)),
			Subject:       "web",
			TriggerStatus: core.StatusDown,
			Mode:          core.HarnessLive,
			StartedAt:     base.Add(time.Duration(i) * time.Minute),
			ToolCalls:     []core.ToolCall{},
			Diagnosis:     core.Diagnosis{Summary: "test"},
		}
		require.NoError(t, s.SaveHarnessCycle(ctx, cycle))
	}

	// List all: should be newest-first (C, B, A).
	all, err := s.ListHarnessCycles(ctx, 100)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.Equal(t, "cycle-C", all[0].ID, "newest first")
	require.Equal(t, "cycle-B", all[1].ID)
	require.Equal(t, "cycle-A", all[2].ID, "oldest last")

	// List limit=1: only C.
	limited, err := s.ListHarnessCycles(ctx, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, "cycle-C", limited[0].ID)
}

// TestStore_GetHarnessCycleMissing: GetHarnessCycle returns an error when
// the cycle id does not exist.
func TestStore_GetHarnessCycleMissing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "missing.db")

	s, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s.Migrate(ctx))
	defer func() { require.NoError(t, s.Close()) }()

	_, err = s.GetHarnessCycle(ctx, "nonexistent")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// TestStore_PruneProbeSamplesDoesNotDeleteHarnessCycles: PruneProbeSamples
// touches only probe_samples, never harness_cycles (ADR 0019 retention
// distinction).
func TestStore_PruneProbeSamplesDoesNotDeleteHarnessCycles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "prune_safety.db")
	now := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	s, err := Open(ctx, path)
	require.NoError(t, err)
	require.NoError(t, s.Migrate(ctx))
	defer func() { require.NoError(t, s.Close()) }()

	// Record an old probe sample.
	require.NoError(t, s.RecordProbe(ctx, core.ProbeSample{
		Service: "web",
		Status:  core.StatusUp,
		Latency: time.Millisecond,
		At:      now.Add(-2 * time.Hour),
	}))

	// Save a harness cycle with an old start time.
	cycle := core.HarnessCycle{
		ID:            "old-cycle",
		Subject:       "web",
		TriggerStatus: core.StatusDown,
		Mode:          core.HarnessLive,
		StartedAt:     now.Add(-2 * time.Hour),
		ToolCalls:     []core.ToolCall{},
		Diagnosis:     core.Diagnosis{Summary: "test"},
	}
	require.NoError(t, s.SaveHarnessCycle(ctx, cycle))

	// Prune probe samples older than 1 hour.
	require.NoError(t, s.PruneProbeSamples(ctx, "web", now.Add(-time.Hour)))

	// Verify the probe sample was pruned.
	probes, err := s.LoadProbeSamples(ctx, "web", 1000)
	require.NoError(t, err)
	require.Len(t, probes, 0, "probe sample should be pruned")

	// Verify the harness cycle was NOT pruned (separate retention class).
	retrieved, err := s.GetHarnessCycle(ctx, "old-cycle")
	require.NoError(t, err)
	require.Equal(t, "old-cycle", retrieved.ID)
}
