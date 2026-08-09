// Package unraid implements core.UnraidSource — the concrete Unraid state
// source (HL-SA-22). Every collector here runs directly on the Unraid host
// (docs/research/unraid-state-sources.md): GraphQL over unraid-api's
// nginx-proxied local endpoint, smartctl and the Docker socket over
// os/exec and a Unix-socket HTTP client, INI files read directly off disk,
// and the tailscale CLI for the reachability self-check.
package unraid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"server-assistant/internal/core"
)

// graphqlClient is a minimal GraphQL-over-HTTP client. Every query this
// source needs lives in this one file — the research doc's "Version
// stability" section confirms unraid-api's schema has broken callers across
// point releases (renamed/retyped fields), so keeping every query and its
// decode target in one place makes that drift a single-file fix.
type graphqlClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func newGraphQLClient(baseURL, apiKey string) *graphqlClient {
	return &graphqlClient{baseURL: baseURL, apiKey: apiKey, http: &http.Client{}}
}

type graphqlRequest struct {
	Query string `json:"query"`
}

type graphqlErrorExtensions struct {
	Code string `json:"code"`
}

type graphqlError struct {
	Message    string                 `json:"message"`
	Extensions graphqlErrorExtensions `json:"extensions"`
}

type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphqlError  `json:"errors,omitempty"`
}

// do executes one GraphQL query bounded by ctx and decodes the "data" object
// into out (out may be nil for a query with no useful payload).
func (c *graphqlClient) do(ctx context.Context, query string, out any) error {
	body, err := json.Marshal(graphqlRequest{Query: query})
	if err != nil {
		return fmt.Errorf("unraid graphql: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("unraid graphql: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		// unraid-api's own public docs (github.com/unraid/api,
		// api/docs/public/how-to-use-the-api.md) document `x-api-key` as the
		// header a programmatic API-key credential is sent on. This is an
		// external, vendor-documented fact, not one reproduced against the
		// reference host: creating a key to prove it end-to-end is the same
		// human-approved host mutation the research doc left open (see
		// docs/research/unraid-state-sources.md, "Open items" #1). The
		// host-verified fact from that doc is the CSRF+session gate this
		// header exists to bypass — confirmed live: an unauthenticated
		// request to a real field returns exactly
		// {"errors":[{"message":"Invalid CSRF token",...,
		// "extensions":{"code":"UNAUTHENTICATED",...}}]}.
		req.Header.Set("x-api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("unraid graphql: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("unraid graphql: http %d: %w", resp.StatusCode, core.ErrUnauthenticated)
	}

	var gr graphqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return fmt.Errorf("unraid graphql: decode response: %w", err)
	}
	if len(gr.Errors) > 0 {
		first := gr.Errors[0]
		if isAuthError(first) {
			return fmt.Errorf("unraid graphql: %s: %w", first.Message, core.ErrUnauthenticated)
		}
		return fmt.Errorf("unraid graphql: %s", first.Message)
	}
	if out != nil {
		if err := json.Unmarshal(gr.Data, out); err != nil {
			return fmt.Errorf("unraid graphql: decode data: %w", err)
		}
	}
	return nil
}

// isAuthError recognizes unraid-api's auth-gate errors: the "extensions.code"
// field is the structured, version-stable signal (confirmed live against the
// reference host: unauthenticated real-field reads return
// extensions.code == "UNAUTHENTICATED"); the message-substring checks are a
// defensive fallback for the "No user session found" case the research doc
// also documented, which was observed without a code field being present.
func isAuthError(e graphqlError) bool {
	if strings.EqualFold(e.Extensions.Code, "UNAUTHENTICATED") || strings.EqualFold(e.Extensions.Code, "FORBIDDEN") {
		return true
	}
	msg := strings.ToLower(e.Message)
	return strings.Contains(msg, "csrf") || strings.Contains(msg, "session") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "unauthenticated")
}

// --- Queries -----------------------------------------------------------
//
// Field names below were confirmed live against the reference host's public,
// unauthenticated GraphQL introspection endpoint (the same technique the
// research doc uses throughout — introspection needs no credential even
// though the data itself does). Values were never fetched live: no API key
// exists yet to authenticate a real query (see UnraidConfig doc comment).

const hostInfoQuery = `{
  info {
    os { hostname uptime }
    cpu { manufacturer brand cores }
  }
  vars { version }
  metrics {
    cpu { percentTotal }
    memory { total used }
  }
}`

type hostInfoResponse struct {
	Info struct {
		OS struct {
			Hostname string `json:"hostname"`
			Uptime   string `json:"uptime"`
		} `json:"os"`
		CPU struct {
			Manufacturer string `json:"manufacturer"`
			Brand        string `json:"brand"`
			Cores        int    `json:"cores"`
		} `json:"cpu"`
	} `json:"info"`
	Vars struct {
		Version string `json:"version"`
	} `json:"vars"`
	Metrics struct {
		CPU struct {
			PercentTotal float64 `json:"percentTotal"`
		} `json:"cpu"`
		Memory struct {
			Total bigInt `json:"total"`
			Used  bigInt `json:"used"`
		} `json:"memory"`
	} `json:"metrics"`
}

const arrayQuery = `{
  array {
    state
    parityCheckStatus { running progress date errors }
    disks { name device size status rotational temp fsSize fsFree fsUsed type isSpinning }
    caches { name device size status rotational temp fsSize fsFree fsUsed type isSpinning }
    parities { name device size status rotational temp fsSize fsFree fsUsed type isSpinning }
  }
  disks { device smartStatus }
}`

type arrayDiskFragment struct {
	Name       string `json:"name"`
	Device     string `json:"device"`
	Size       bigInt `json:"size"`
	Status     string `json:"status"`
	Rotational bool   `json:"rotational"`
	Temp       *int   `json:"temp"`
	FsSize     bigInt `json:"fsSize"`
	FsFree     bigInt `json:"fsFree"`
	FsUsed     bigInt `json:"fsUsed"`
	Type       string `json:"type"`
	IsSpinning *bool  `json:"isSpinning"`
}

type arrayResponse struct {
	Array struct {
		State             string `json:"state"`
		ParityCheckStatus struct {
			Running  bool    `json:"running"`
			Progress *int    `json:"progress"`
			Date     *string `json:"date"`
			Errors   *int64  `json:"errors"`
		} `json:"parityCheckStatus"`
		Disks    []arrayDiskFragment `json:"disks"`
		Caches   []arrayDiskFragment `json:"caches"`
		Parities []arrayDiskFragment `json:"parities"`
	} `json:"array"`
	// Disks is the top-level Query.disks field (distinct GraphQL type from
	// array.disks/caches/parities) — the only place smartStatus lives, per
	// the research doc's schema-coverage table.
	Disks []struct {
		Device      string `json:"device"`
		SmartStatus string `json:"smartStatus"`
	} `json:"disks"`
}

const sharesQuery = `{
  shares { name size free used allocator }
}`

type sharesResponse struct {
	Shares []struct {
		Name      string `json:"name"`
		Size      bigInt `json:"size"`
		Free      bigInt `json:"free"`
		Used      bigInt `json:"used"`
		Allocator string `json:"allocator"`
	} `json:"shares"`
}

// bigInt decodes unraid-api's BigInt custom scalar, which the research doc's
// "Version stability" section warns has changed JSON representation
// (int <-> string) across releases (e.g. ParityCheck.speed). Accepting both
// shapes defensively means a future scalar-encoding change degrades to a
// parse error on the affected field rather than breaking every query in this
// file that touches a BigInt.
type bigInt int64

func (b *bigInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		*b = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("unraid graphql: decode BigInt %q: %w", s, err)
	}
	*b = bigInt(v)
	return nil
}
