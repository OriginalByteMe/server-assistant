package prober

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// SSHConfig is the resolved connection material for the Host SSH Runner. The
// credential is a scoped, non-root, read-only Unraid user (CONVENTIONS rule
// 7 / ADR 0003 hygiene). Password / PrivateKey arrive already resolved from
// env or a file by the config layer — never the committed YAML — and are
// never logged (rule 8): they are not interpolated into any error or log.
type SSHConfig struct {
	Address    string // host:port
	User       string
	Password   string // optional; one of Password / PrivateKey
	PrivateKey []byte // optional PEM; preferred for a non-interactive probe user
	Timeout    time.Duration
	// HostKeyCallback verifies the Host's key. Nil ⇒ insecure accept-any,
	// which v1 deliberately tolerates for a homelab (ADR 0003 defers security
	// hardening to M2); the wiring layer logs a one-time warning.
	HostKeyCallback gossh.HostKeyCallback
}

// SSHClient is the real Runner: it opens a fresh SSH connection per Probe,
// runs one bounded read-only command, and tears the connection down. A fresh
// connection per probe keeps the adapter stateless and restart-safe; the poll
// interval makes per-probe dial cost a non-issue (the tiebreaker rule favours
// the simpler, obviously-correct design).
type SSHClient struct {
	cfg SSHConfig
}

var _ Runner = (*SSHClient)(nil)

func NewSSHClient(cfg SSHConfig) *SSHClient { return &SSHClient{cfg: cfg} }

func (c *SSHClient) authMethods() ([]gossh.AuthMethod, error) {
	var m []gossh.AuthMethod
	if len(c.cfg.PrivateKey) > 0 {
		signer, err := gossh.ParsePrivateKey(c.cfg.PrivateKey)
		if err != nil {
			// Do not include the key material in the error (rule 8).
			return nil, fmt.Errorf("parse ssh private key: invalid PEM")
		}
		m = append(m, gossh.PublicKeys(signer))
	}
	if c.cfg.Password != "" {
		m = append(m, gossh.Password(c.cfg.Password))
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("ssh: no credential configured (need key or password)")
	}
	return m, nil
}

// Run dials, authenticates, runs cmd, and returns its combined output, all
// bounded by an explicit timeout/context (CONVENTIONS rule 4). The whole
// operation is raced against ctx: if it elapses, the underlying connection is
// closed so a hung Host can never hang the probe (and never yields DOWN —
// the caller treats the error as "can't tell", rule 5 / ADR 0005).
func (c *SSHClient) Run(ctx context.Context, cmd string) (string, error) {
	hostKey := c.cfg.HostKeyCallback
	if hostKey == nil {
		hostKey = gossh.InsecureIgnoreHostKey() //nolint:gosec // ADR 0003: v1 defers host-key pinning; wiring warns
	}
	auths, err := c.authMethods()
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	d := net.Dialer{Timeout: c.cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", c.cfg.Address)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", c.cfg.Address, err)
	}
	// Closing the conn unblocks the handshake/session goroutine on ctx
	// expiry. Safe to call twice (net.Conn.Close is idempotent enough here).
	defer func() { _ = conn.Close() }()

	type result struct {
		out string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		clientCfg := &gossh.ClientConfig{
			User:            c.cfg.User,
			Auth:            auths,
			HostKeyCallback: hostKey,
			Timeout:         c.cfg.Timeout,
		}
		sc, chans, reqs, err := gossh.NewClientConn(conn, c.cfg.Address, clientCfg)
		if err != nil {
			resCh <- result{err: fmt.Errorf("ssh handshake %s: %w", c.cfg.Address, err)}
			return
		}
		client := gossh.NewClient(sc, chans, reqs)
		defer func() { _ = client.Close() }()

		sess, err := client.NewSession()
		if err != nil {
			resCh <- result{err: fmt.Errorf("ssh session: %w", err)}
			return
		}
		defer func() { _ = sess.Close() }()

		var buf bytes.Buffer
		sess.Stdout = &buf
		sess.Stderr = &buf
		if err := sess.Run(cmd); err != nil {
			resCh <- result{out: buf.String(), err: fmt.Errorf("ssh run: %w", err)}
			return
		}
		resCh <- result{out: buf.String()}
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close() // unblock the goroutine; it will error out and drain
		return "", fmt.Errorf("ssh %s: %w", c.cfg.Address, ctx.Err())
	case r := <-resCh:
		return r.out, r.err
	}
}
