package mcp

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// bearerPrefix is the RFC 6750 scheme prefix this server accepts on the
// Authorization header. Case-sensitive per the RFC's own grammar.
const bearerPrefix = "Bearer "

// checkAuth reports whether r carries the exact bearer token configured on
// s. Only ever called when s.authToken != "" — an empty authToken means
// auth is off (serveHTTP gates that case before reaching here; see
// NewServer's startup WARN for that path).
//
// Uses subtle.ConstantTimeCompare rather than ==: this endpoint fronts a
// root-equivalent Docker-socket surface (issue #51's own correction — the
// container boundary is a distribution convenience, not a security
// boundary), so leaking which byte of a guessed token is wrong via
// response-timing analysis is not an acceptable risk here, even though a
// plain == is fine for the non-secret comparisons elsewhere in this
// codebase.
//
// Swap seam for issue #60's settled end state: once an unraid-api API key
// exists on the host, replace the ConstantTimeCompare call below with a
// call that validates the presented credential against unraid-api (roles +
// per-resource permissions) instead of a shared secret. The bearer-token
// extraction above it, and writeUnauthorized's "identical response either
// way" contract, do not need to change.
func (s *Server) checkAuth(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, bearerPrefix) {
		return false
	}
	provided := strings.TrimPrefix(h, bearerPrefix)
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.authToken)) == 1
}

// writeUnauthorized answers a missing or incorrect bearer token. It is the
// ONLY place that produces this response, and it is built from nothing but
// static literals — no request-derived data — so a missing header and a
// wrong one are byte-for-byte indistinguishable to the caller. That's
// deliberate: telling them apart would hand an attacker a free oracle for
// "am I merely unauthenticated, or actively guessing wrong".
func (s *Server) writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage("null"),
		Error:   &rpcError{Code: codeUnauthorized, Message: "unauthorized: missing or invalid bearer token"},
	})
}
