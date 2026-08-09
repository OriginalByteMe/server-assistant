package scripts

import "context"

// CheckResult is what an LLM learns from check(text) — validity and
// rejection reasons for exactly the text it submitted.
type CheckResult struct {
	Valid    bool
	Reasons  []string
	Warnings []string
}

// Check runs the same dry-run rejection predicate against arbitrary
// submitted text and reports only what that text's own dry run showed.
//
// Coordinator decision C6 (GitHub #55) treats this as a security boundary:
// check(text) must never reveal whether a matching script already exists or
// is granted, because either would make it a hash oracle over the grant
// store. The guarantee here is structural, not behavioural — Check takes no
// Store, no Registry, nothing that could look anything up by hash. It is a
// pure function of (executor, text): given the same text, it produces the
// same verdict whether or not that exact text happens to already be a
// stored, approved script, because there is no code path here that could
// even ask.
func Check(ctx context.Context, exec *Executor, text string) (CheckResult, error) {
	res, err := exec.DryRun(ctx, text)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Valid: res.Approved, Reasons: res.Reasons, Warnings: res.Warnings}, nil
}
