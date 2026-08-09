//go:build demo

// Package demo_test drives the M2 harness demo end to end against a live
// deployment: sa-demo-web dies on Unraid, the harness diagnoses it via
// Ollama, an operator approves the proposed restart on the web dashboard,
// the SSH actuator restarts the container, and the spine observes recovery.
// It is gated behind the `demo` build tag because it requires live network
// access to the sa-dev harness box and the Unraid host, and it mutates a
// real (throwaway) container on Unraid.
package demo_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ToolCall mirrors core.ToolCall as serialized by internal/web.
type ToolCall struct {
	Tool       string `json:"tool"`
	Args       any    `json:"args"`
	Output     string `json:"output"`
	Err        string `json:"err"`
	At         string `json:"at"`
	DurationMs int64  `json:"duration_ms"`
}

// Proposal mirrors core.Proposal.
type Proposal struct {
	Kind      string `json:"kind"`
	Subject   string `json:"subject"`
	Rationale string `json:"rationale"`
}

// Usage mirrors core.ReasonUsage.
type Usage struct {
	Backend          string `json:"backend"`
	Model            string `json:"model"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	LatencyMs        int64  `json:"latency_ms"`
}

// Diagnosis mirrors core.Diagnosis.
type Diagnosis struct {
	Summary  string   `json:"summary"`
	Proposed Proposal `json:"proposed"`
	Usage    Usage    `json:"usage"`
	Fallback bool     `json:"fallback"`
}

// Incident mirrors the JSON shape of core.HarnessCycle as serialized by
// internal/web's incident API (see internal/web response contract).
type Incident struct {
	ID             string     `json:"id"`
	Subject        string     `json:"subject"`
	TriggerStatus  string     `json:"trigger_status"`
	Mode           string     `json:"mode"`
	StartedAt      string     `json:"started_at"`
	ToolCalls      []ToolCall `json:"tool_calls"`
	Diagnosis      Diagnosis  `json:"diagnosis"`
	Approval       string     `json:"approval"`
	ApprovedBy     string     `json:"approved_by"`
	ApprovedAt     *string    `json:"approved_at"`
	ResolvedTarget string     `json:"resolved_target"`
	DispatchResult string     `json:"dispatch_result"`
	DispatchedAt   *string    `json:"dispatched_at"`
	Outcome        string     `json:"outcome"`
	OutcomeAt      *string    `json:"outcome_at"`
	Error          string     `json:"error"`
}

// Health mirrors the /api/health response.
type Health struct {
	Status        string `json:"status"`
	HarnessMode   string `json:"harness_mode"`
	HarnessHalted bool   `json:"harness_halted"`
}

// sshOut runs `ssh -o BatchMode=yes <host> <cmd>` and returns trimmed
// combined output. It fails the test on a non-zero exit unless failOnErr is
// false, in which case errors are swallowed (used for best-effort cleanup).
func sshOut(t *testing.T, host, cmd string, failOnErr bool) string {
	t.Helper()
	c := exec.Command("ssh", "-o", "BatchMode=yes", host, cmd)
	out, err := c.CombinedOutput()
	if err != nil && failOnErr {
		t.Fatalf("ssh %s %q: %v\noutput: %s", host, cmd, err, out)
	}
	return strings.TrimSpace(string(out))
}

// getJSON GETs url with a 10s client and decodes the body into out when the
// response is 200. It always returns the observed status code.
func getJSON(t *testing.T, url string, out any) int {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	require.NoError(t, err, "GET %s", url)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && out != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out), "decode GET %s", url)
	}
	return resp.StatusCode
}

// postJSON POSTs to url with a 10s client and decodes the body into out when
// the response is 200. It always returns the observed status code.
func postJSON(t *testing.T, url string, out any) int {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(nil))
	require.NoError(t, err, "POST %s", url)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && out != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(out), "decode POST %s", url)
	}
	return resp.StatusCode
}

// poll calls fn every interval until it returns true or timeout elapses. On
// timeout it fails the test with desc and the last observed body so a
// failure is debuggable without reproducing the run.
func poll(t *testing.T, timeout, interval time.Duration, desc string, fn func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		ok, body := fn()
		last = body
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: timed out after %s; last=%s", desc, timeout, last)
		}
		time.Sleep(interval)
	}
}

func TestDemoE2E(t *testing.T) {
	container := envOr("SA_DEMO_CONTAINER", "sa-demo-web")
	// Safety rail: every docker command below interpolates only this
	// validated name, so the test is structurally incapable of touching a
	// non-demo (production) container.
	if !strings.HasPrefix(container, "sa-demo-") {
		t.Fatalf("SA_DEMO_CONTAINER %q must have prefix \"sa-demo-\"; refusing to run against a non-demo container", container)
	}

	base := envOr("SA_BASE_URL", "http://192.168.68.61:8080")
	unraid := envOr("SA_UNRAID", "rijkaardserver")
	service := envOr("SA_DEMO_SERVICE", "demo-web")

	t.Cleanup(func() {
		sshOut(t, unraid, fmt.Sprintf("docker rm -f %s", container), false)
	})

	// Step 2: (re)create the demo container fresh on Unraid.
	sshOut(t, unraid, fmt.Sprintf("docker rm -f %s", container), false)
	sshOut(t, unraid, fmt.Sprintf(
		"docker run -d --name %s --memory 64m --cpus 0.25 --restart=no busybox sleep infinity",
		container), true)

	// Step 3: harness API must be reachable and reporting ok.
	poll(t, 60*time.Second, 2*time.Second, "waiting for /api/health", func() (bool, string) {
		var h Health
		status := getJSON(t, base+"/api/health", &h)
		body := fmt.Sprintf("status=%d health=%+v", status, h)
		return status == http.StatusOK && h.Status == "ok", body
	})

	// Step 4: record baseline incident ids so we can identify the new one.
	var baseline []Incident
	require.Equal(t, http.StatusOK, getJSON(t, base+"/api/incidents?limit=50", &baseline),
		"fetching baseline incidents")
	baselineIDs := make(map[string]bool, len(baseline))
	for _, inc := range baseline {
		baselineIDs[inc.ID] = true
	}

	// Step 4b: clear any still-outstanding Approval. The harness serialises
	// cycles globally (ADR 0017): while one incident awaits an Operator, a
	// fresh DOWN commit is deliberately dropped, so a leftover pending cycle
	// from an earlier run would make this test hang instead of fail. Denying
	// it is exactly what an Operator would do, and it uses the same API
	// surface the demo is proving.
	for _, inc := range baseline {
		if inc.Approval != "pending" {
			continue
		}
		var cleared Incident
		status := postJSON(t, fmt.Sprintf("%s/api/incidents/%s/deny?who=demo-e2e-setup", base, inc.ID), &cleared)
		t.Logf("cleared outstanding pending incident %s (status=%d approval=%q)", inc.ID, status, cleared.Approval)
	}

	// Step 5: let the spine commit UP first, then let the debounced commit
	// land before we knock the container down. The status prober polls
	// every poll_interval (5s) and only commits a transition after
	// debounce_n (2) consecutive matching reads, so we wait for the
	// container to report running and then give it two poll cycles' worth
	// of margin (~15s) before triggering DOWN.
	poll(t, 60*time.Second, 3*time.Second, "waiting for container to report running", func() (bool, string) {
		out := sshOut(t, unraid, fmt.Sprintf("docker inspect --format {{.State.Running}} %s", container), true)
		return out == "true", out
	})
	time.Sleep(15 * time.Second)

	// Step 6: trigger the incident.
	sshOut(t, unraid, fmt.Sprintf("docker stop %s", container), true)

	// Step 7: wait for the harness to produce a new incident for our service
	// AND finish diagnosing it. The harness persists a cycle as soon as it
	// starts so the dashboard can show it progressing, so "an incident exists"
	// is not the same as "the Diagnosis is done". The state this test wants is
	// the one an Operator can act on: a proposal is on the record and the
	// cycle is awaiting Approval. On this box the local model takes ~20s to
	// answer, so the incident is visible well before it is actionable.
	var incidentID string
	var incident Incident
	poll(t, 240*time.Second, 5*time.Second, "waiting for a diagnosed DOWN incident awaiting approval", func() (bool, string) {
		var incidents []Incident
		status := getJSON(t, base+"/api/incidents?limit=50", &incidents)
		if status != http.StatusOK {
			return false, fmt.Sprintf("status=%d", status)
		}
		for _, inc := range incidents {
			if baselineIDs[inc.ID] || inc.Subject != service {
				continue
			}
			incidentID = inc.ID
			incident = inc
			if inc.Diagnosis.Proposed.Kind != "" {
				return true, ""
			}
		}
		body, _ := json.Marshal(incidents)
		return false, string(body)
	})
	require.NotEmpty(t, incidentID, "expected a new incident id for subject %q", service)
	require.Equal(t, "DOWN", incident.TriggerStatus, "incident trigger_status")
	require.Equal(t, "restart_container", incident.Diagnosis.Proposed.Kind, "diagnosis.proposed.kind")
	require.NotEmpty(t, incident.Diagnosis.Usage.Model, "diagnosis.usage.model must be set")
	require.Greater(t, incident.Diagnosis.Usage.LatencyMs, int64(0), "diagnosis.usage.latency_ms must be positive")
	require.GreaterOrEqual(t, len(incident.ToolCalls), 1, "expected at least one read tool call")
	require.Equal(t, "pending", incident.Approval, "incident approval")

	// Step 8: incident detail HTML page renders the incident and an Approve action.
	{
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(fmt.Sprintf("%s/incidents/%s", base, incidentID))
		require.NoError(t, err, "GET /incidents/%s", incidentID)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "GET /incidents/%s status", incidentID)
		var buf bytes.Buffer
		_, err = buf.ReadFrom(resp.Body)
		require.NoError(t, err, "reading /incidents/%s body", incidentID)
		html := buf.String()
		require.Contains(t, html, incidentID, "incident detail page must contain the incident id")
		require.Contains(t, html, "Approve", "incident detail page must offer an Approve action")
	}

	// Step 9: operator approves the proposed remediation.
	var approved Incident
	approveStatus := postJSON(t,
		fmt.Sprintf("%s/api/incidents/%s/approve?who=demo-e2e", base, incidentID), &approved)
	require.Equal(t, http.StatusOK, approveStatus, "POST approve status")
	require.Equal(t, "approved", approved.Approval, "approval after approve")

	// Step 10: wait for the actuator to dispatch the restart and the spine
	// to observe recovery.
	var recovered Incident
	poll(t, 240*time.Second, 5*time.Second, "waiting for outcome=recovered", func() (bool, string) {
		var inc Incident
		status := getJSON(t, fmt.Sprintf("%s/api/incidents/%s", base, incidentID), &inc)
		if status != http.StatusOK {
			return false, fmt.Sprintf("status=%d", status)
		}
		recovered = inc
		if inc.Outcome == "recovered" {
			return true, ""
		}
		body, _ := json.Marshal(inc)
		return false, string(body)
	})
	require.Equal(t, container, recovered.ResolvedTarget, "resolved_target")
	require.Equal(t, "dispatched", recovered.DispatchResult, "dispatch_result")

	// Step 11: the container is actually running again on Unraid.
	running := sshOut(t, unraid,
		fmt.Sprintf("docker inspect --format {{.State.Running}} %s", container), true)
	require.Equal(t, "true", running, "container must be running after recovery")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
