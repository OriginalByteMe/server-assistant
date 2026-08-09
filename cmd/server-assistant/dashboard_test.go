package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"server-assistant/internal/harness"
)

// A nil *harness.Harness must never reach internal/web as a non-nil
// HarnessSource. Assigning a nil pointer into an interface produces a
// non-nil interface value, so web's `hs != nil` guards all pass and the
// always-registered /api/health route dereferences nil and panics —
// violating CONVENTIONS rule 10 ("daemons don't panic") and crash-looping
// the container, because the Docker healthcheck hits exactly that route.
//
// This escaped every package test: they all inject a real fake HarnessSource,
// so only the composition root can produce the typed-nil. It was found by
// deploying to the Unraid host and reading the crash loop.
func TestDashboard_NilHarnessDoesNotPanicOnHealth(t *testing.T) {
	var hs *harness.Harness // deliberately typed nil, as run() has when cfg.Harness is absent

	h := dashboard(nil, hs, nil, nil, nil)

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	})
	require.Equal(t, http.StatusOK, rec.Code)
}
