package mcp

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// defaultWindowHours and maxWindowHours bound get_smart_trend's hours/days
// argument. maxWindowHours mirrors the sampler's retention window (GitHub
// #61, config.SamplerConfig.Retention — 90 days): asking for more history
// than the store keeps is a malformed call, not a business failure.
const (
	defaultWindowHours = 24
	maxWindowHours     = 90 * 24
)

func registerTrendTools(s *Server, trends TrendSource) {
	s.Register(Tool{
		Name:        "list_trend_series",
		Category:    "trends",
		Description: "Every (series, subject) pair the sampler has actually recorded history for — e.g. smart.reallocated_sector_ct/dev/sdb, capacity.disk/dev/sdb, array.state/array. Call this before get_smart_trend: series names are not guessable.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
		},
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error) {
			pairs, err := trends.ListTrendSeries(ctx)
			if err != nil {
				return ToolResult{}, err
			}
			out := make([]map[string]any, len(pairs))
			for i, p := range pairs {
				out[i] = map[string]any{"series": p.Series, "subject": p.Subject}
			}
			return renderResult(map[string]any{"series": out})
		},
	})

	s.Register(Tool{
		Name:        "get_smart_trend",
		Category:    "trends",
		Description: "History for one stored series (SMART attribute, disk/share capacity, or array state) and subject over a recent window — the whole point of sampling at all (GitHub #61): one reading is meaningless, a trend across readings is the signal. Gaps (e.g. a disk skipped in standby) are reported explicitly and never interpolated; a gap-heavy window is flagged rather than presented as a confident trend. Call list_trend_series first to find valid series/subject values.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"series": map[string]any{
					"type":        "string",
					"description": "dotted series name from list_trend_series, e.g. smart.reallocated_sector_ct or capacity.disk",
				},
				"subject": map[string]any{
					"type":        "string",
					"description": "device/share/array subject from list_trend_series, e.g. /dev/sdb",
				},
				"hours": map[string]any{
					"type":        "integer",
					"description": "window size in whole hours, ending now. Default 24, max 2160 (90 days, the sampler's retention). Mutually exclusive with days.",
				},
				"days": map[string]any{
					"type":        "integer",
					"description": "window size in whole days, ending now. Mutually exclusive with hours.",
				},
				"detail": map[string]any{
					"type":        "boolean",
					"description": "return every point in the window, gaps included, instead of just the summary",
				},
			},
			"required":             []string{"series", "subject"},
			"additionalProperties": false,
		},
		Required:    []string{"series", "subject"},
		Annotations: Annotations{ReadOnlyHint: true, IdempotentHint: true},
		Handler: func(ctx context.Context, args map[string]any, detail bool) (ToolResult, error) {
			window, werr := trendWindow(args)
			if werr != nil {
				return ToolResult{}, werr
			}
			series := stringArg(args, "series")
			subject := stringArg(args, "subject")

			pairs, err := trends.ListTrendSeries(ctx)
			if err != nil {
				return ToolResult{}, err
			}
			if !knownSeries(pairs, series) {
				return structuredError(
					"unknown_series",
					fmt.Sprintf("no stored history for series %q", series),
					validSeriesNames(pairs)...,
				), nil
			}

			to := time.Now().UTC()
			from := to.Add(-window)
			points, err := trends.Trend(ctx, series, subject, from, to)
			if err != nil {
				return ToolResult{}, err
			}
			if detail {
				return renderResult(trendDetailView(series, subject, from, to, points))
			}
			return renderResult(trendSummaryView(series, subject, from, to, points))
		},
	})
}

// trendWindow resolves get_smart_trend's hours/days arguments into a
// duration, or an *InvalidParamsError (-32602) for a malformed one: both
// given, neither a positive whole number, or beyond the sampler's
// retention window.
func trendWindow(args map[string]any) (time.Duration, error) {
	hoursRaw, hasHours := args["hours"]
	daysRaw, hasDays := args["days"]
	if hasHours && hasDays {
		return 0, &InvalidParamsError{Message: "provide at most one of hours or days, not both"}
	}

	hours := float64(defaultWindowHours)
	switch {
	case hasHours:
		v, ok := wholePositiveNumber(hoursRaw)
		if !ok {
			return 0, &InvalidParamsError{Message: "hours must be a positive whole number"}
		}
		hours = v
	case hasDays:
		v, ok := wholePositiveNumber(daysRaw)
		if !ok {
			return 0, &InvalidParamsError{Message: "days must be a positive whole number"}
		}
		hours = v * 24
	}
	if hours > maxWindowHours {
		return 0, &InvalidParamsError{Message: fmt.Sprintf(
			"window exceeds the sampler's %dh (%dd) retention; ask for a smaller hours/days value",
			maxWindowHours, maxWindowHours/24,
		)}
	}
	return time.Duration(hours * float64(time.Hour)), nil
}

func wholePositiveNumber(v any) (float64, bool) {
	f, ok := v.(float64) // every JSON number decodes to float64 through map[string]any
	if !ok || f <= 0 || f != math.Trunc(f) {
		return 0, false
	}
	return f, true
}

func knownSeries(pairs []TrendSeriesInfo, series string) bool {
	for _, p := range pairs {
		if p.Series == series {
			return true
		}
	}
	return false
}

func validSeriesNames(pairs []TrendSeriesInfo) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if !seen[p.Series] {
			seen[p.Series] = true
			out = append(out, p.Series)
		}
	}
	sort.Strings(out)
	return out
}

// pointValue returns a TrendPoint's payload as whichever concrete type it
// actually carries (numeric or text), or nil for a gap — never a fake
// zero (CONVENTIONS rule 5).
func pointValue(p TrendPoint) any {
	switch {
	case p.Value != nil:
		return *p.Value
	case p.Text != nil:
		return *p.Text
	default:
		return nil
	}
}

// trendSummaryView is get_smart_trend's default (non-detail) projection:
// first/last/delta/direction plus explicit sample and gap counts. A
// gap-heavy window (majority gaps, or fewer than two confident samples)
// reports direction "insufficient-data" rather than computing a misleading
// trend from too little confirmed history.
func trendSummaryView(series, subject string, from, to time.Time, points []TrendPoint) map[string]any {
	sampleCount, gapCount := 0, 0
	var firstOK, lastOK *TrendPoint
	for i := range points {
		p := &points[i]
		if !p.OK {
			gapCount++
			continue
		}
		sampleCount++
		if firstOK == nil {
			firstOK = p
		}
		lastOK = p
	}

	out := map[string]any{
		"series":       series,
		"subject":      subject,
		"from":         from.Format(time.RFC3339),
		"to":           to.Format(time.RFC3339),
		"sample_count": sampleCount,
		"gap_count":    gapCount,
	}

	gapHeavy := sampleCount < 2 || gapCount > sampleCount
	out["gap_heavy"] = gapHeavy

	if sampleCount == 0 {
		out["direction"] = "insufficient-data"
		out["note"] = fmt.Sprintf("no readings in this window (%d gaps) — nothing to trend yet.", gapCount)
		return out
	}

	out["first"] = pointValue(*firstOK)
	out["first_at"] = firstOK.At.Format(time.RFC3339)
	out["last"] = pointValue(*lastOK)
	out["last_at"] = lastOK.At.Format(time.RFC3339)

	if gapHeavy {
		out["direction"] = "insufficient-data"
		out["note"] = fmt.Sprintf(
			"%d of %d points in this window are gaps (skipped reads, e.g. a disk in standby) — not enough confident samples for a reliable trend.",
			gapCount, gapCount+sampleCount,
		)
		return out
	}

	out["note"] = fmt.Sprintf("%d samples, %d gaps in this window.", sampleCount, gapCount)
	switch {
	case firstOK.Value != nil && lastOK.Value != nil:
		delta := *lastOK.Value - *firstOK.Value
		out["delta"] = delta
		switch {
		case delta > 0:
			out["direction"] = "up"
		case delta < 0:
			out["direction"] = "down"
		default:
			out["direction"] = "flat"
		}
	case firstOK.Text != nil && lastOK.Text != nil:
		if *firstOK.Text == *lastOK.Text {
			out["direction"] = "unchanged"
		} else {
			out["direction"] = "changed"
		}
	default:
		out["direction"] = "insufficient-data"
	}
	return out
}

// trendDetailView is get_smart_trend's detail:true projection: the full
// ordered point list, gaps included exactly as recorded (ok:false, no
// value/text field) — render()'s byte cap still applies on the way out.
func trendDetailView(series, subject string, from, to time.Time, points []TrendPoint) map[string]any {
	pts := make([]map[string]any, len(points))
	for i, p := range points {
		pv := map[string]any{
			"at": p.At.Format(time.RFC3339),
			"ok": p.OK,
		}
		if p.Value != nil {
			pv["value"] = *p.Value
		}
		if p.Text != nil {
			pv["text"] = *p.Text
		}
		if p.Note != "" {
			pv["note"] = p.Note
		}
		pts[i] = pv
	}
	return map[string]any{
		"series":  series,
		"subject": subject,
		"from":    from.Format(time.RFC3339),
		"to":      to.Format(time.RFC3339),
		"points":  pts,
	}
}
