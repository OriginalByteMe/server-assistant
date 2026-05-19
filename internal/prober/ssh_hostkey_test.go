package prober

import (
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// An empty host_key means v1 accept-any (ADR 0003 defers host-key pinning):
// ParseHostKey returns a usable, non-nil callback so the probe still works,
// and reports insecure=true so the wiring layer can warn once.
func TestParseHostKey_EmptyIsInsecureAcceptAny(t *testing.T) {
	cb, insecure, err := ParseHostKey("")
	require.NoError(t, err)
	require.NotNil(t, cb)
	require.True(t, insecure)
}

// A configured authorized-key line pins the Host: the callback accepts that
// exact key and rejects any other (real host-key verification).
func TestParseHostKey_PinnedAcceptsOnlyThatKey(t *testing.T) {
	pubA, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	sshPubA, err := gossh.NewPublicKey(pubA)
	require.NoError(t, err)
	authorized := string(gossh.MarshalAuthorizedKey(sshPubA))

	cb, insecure, err := ParseHostKey(authorized)
	require.NoError(t, err)
	require.False(t, insecure)
	require.NoError(t, cb("h:22", nil, sshPubA), "the pinned key is accepted")

	pubB, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	sshPubB, err := gossh.NewPublicKey(pubB)
	require.NoError(t, err)
	require.Error(t, cb("h:22", nil, sshPubB), "any other key is rejected")
}

func TestParseHostKey_GarbageIsError(t *testing.T) {
	_, _, err := ParseHostKey("not-a-valid-authorized-key")
	require.Error(t, err)
}
