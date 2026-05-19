package prober

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// newTestSSHServer starts an in-process SSH server on loopback (no external
// network — CONVENTIONS rule 9). It accepts user "probe" with password
// "s3cret", and for every exec request writes handler(cmd) to stdout then
// exits 0. Returns the listen address and the server host key.
func newTestSSHServer(t *testing.T, handler func(cmd string) string) (addr string, hostKey gossh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := gossh.NewSignerFromKey(priv)
	require.NoError(t, err)

	cfg := &gossh.ServerConfig{
		PasswordCallback: func(c gossh.ConnMetadata, pw []byte) (*gossh.Permissions, error) {
			if c.User() == "probe" && string(pw) == "s3cret" {
				return &gossh.Permissions{}, nil
			}
			return nil, errors.New("auth failed")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(nc, cfg, handler)
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func serveSSHConn(nc net.Conn, cfg *gossh.ServerConfig, handler func(string) string) {
	conn, chans, reqs, err := gossh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	go gossh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(gossh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range chReqs {
				if req.Type == "exec" {
					var p struct{ Command string }
					_ = gossh.Unmarshal(req.Payload, &p)
					_, _ = io.WriteString(ch, handler(p.Command))
					if req.WantReply {
						_ = req.Reply(true, nil)
					}
					_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ S uint32 }{0}))
					_ = ch.Close()
					return
				}
				if req.WantReply {
					_ = req.Reply(false, nil)
				}
			}
		}()
	}
}

// The SSH client connects with the configured (non-root, read-only) user and
// secret, runs one bounded read-only command, and returns its stdout — the
// real Runner behind the ContainerProbe / HostMetricsProbe seam.
func TestSSHClient_RunsCommandAndReturnsStdout(t *testing.T) {
	addr, hostKey := newTestSSHServer(t, func(cmd string) string {
		return "ran:" + cmd
	})

	c := NewSSHClient(SSHConfig{
		Address:         addr,
		User:            "probe",
		Password:        "s3cret",
		Timeout:         2 * time.Second,
		HostKeyCallback: gossh.FixedHostKey(hostKey),
	})
	out, err := c.Run(context.Background(), "docker inspect plex")
	require.NoError(t, err)
	require.Equal(t, "ran:docker inspect plex", out)
}

// Wrong credentials fail loudly (no silent empty success), and the error must
// not leak the secret.
func TestSSHClient_AuthFailureErrorsWithoutLeakingSecret(t *testing.T) {
	addr, hostKey := newTestSSHServer(t, func(string) string { return "" })

	c := NewSSHClient(SSHConfig{
		Address:         addr,
		User:            "probe",
		Password:        "WRONG-PASSWORD",
		Timeout:         2 * time.Second,
		HostKeyCallback: gossh.FixedHostKey(hostKey),
	})
	_, err := c.Run(context.Background(), "whoami")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "WRONG-PASSWORD", "the secret must never appear in errors (rule 8)")
}

// Every SSH call is bounded by an explicit timeout/context (CONVENTIONS rule
// 4): a hung server does not hang the probe.
func TestSSHClient_ContextTimeoutIsEnforced(t *testing.T) {
	addr, hostKey := newTestSSHServer(t, func(string) string {
		time.Sleep(2 * time.Second) // slower than the client timeout
		return "too late"
	})

	c := NewSSHClient(SSHConfig{
		Address:         addr,
		User:            "probe",
		Password:        "s3cret",
		Timeout:         100 * time.Millisecond,
		HostKeyCallback: gossh.FixedHostKey(hostKey),
	})

	done := make(chan struct{})
	var err error
	go func() {
		_, err = c.Run(context.Background(), "sleep 9000")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return — context timeout not enforced (rule 4)")
	}
	require.Error(t, err)
}
