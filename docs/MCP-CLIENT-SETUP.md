# Connecting an MCP client to Server Assistant

Server Assistant exposes one Streamable HTTP MCP endpoint at
`http://100.90.134.29:8099/mcp` (or `http://rijkaardserver.tail8c2c85.ts.net:8099/mcp`
by hostname) on the tailnet. Two ways to reach it from a client, per issue
#56's resolution:

| Route | What it requires | What it exposes | Which clients work |
|---|---|---|---|
| **Local stdio shim** (this doc, fallback) | Your own machine joined to the tailnet (or on the same LAN as the Unraid box) | Nothing — no inbound port opens anywhere, Funnel is never touched | Any client that can spawn a local stdio MCP server: **Claude Desktop's local server config**, Claude Code's `.mcp.json`. Does **not** work with `claude.ai` (web) or the Claude mobile app — both only speak to remote HTTP servers from Anthropic's own cloud infrastructure. |
| **Tailscale Funnel** (primary, not built by this ticket) | A human enabling `tailscale funnel` on the Unraid host (denied to agents; see issue #56 §3) | The dashboard and MCP endpoint become reachable by **anyone on the public internet** who has or guesses the URL — Funnel authenticates the transport (valid TLS, no port-forward hole), not the caller | `claude.ai`, Claude mobile, Claude Desktop's remote connector — any client that reaches servers from Anthropic's cloud |

Use the shim if you are unwilling to expose anything publicly. Use Funnel
only if you understand and accept that public-exposure trade — see
`docs/research/mcp-reachability.md` §3 for the exact caveats.

## Option A — local stdio shim (recommended, no public exposure)

### 1. Build the shim

```
cd server-assistant
make shim
```

This produces a static binary at `bin/sa-mcp-shim` (`CGO_ENABLED=0`, no
runtime dependencies — copy it anywhere). This ticket's proof binary lived at
the absolute path:

```
/home/noahr/Projects/server-assistant/bin/sa-mcp-shim
```

Use **your own machine's absolute path** to wherever you place the binary in
the config below — a relative path will not resolve when the client spawns
the process from an unrelated working directory.

### 2. Register it with your client

Verified against Anthropic's current MCP client docs (checked
2026-08-09): [Connect to local MCP servers](https://modelcontextprotocol.io/docs/develop/connect-local-servers)
for the `claude_desktop_config.json` `mcpServers` block shape
(`command`/`args`/`env`, keyed by server name), and
[Claude Code: Connect Claude Code to tools via MCP](https://code.claude.com/docs/en/mcp)
for the `.mcp.json` / `claude mcp add` stdio-server form.

**Claude Desktop** — edit (or create)
`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS or
`%APPDATA%\Claude\claude_desktop_config.json` on Windows:

```json
{
  "mcpServers": {
    "server-assistant": {
      "command": "/home/noahr/Projects/server-assistant/bin/sa-mcp-shim",
      "args": [
        "-endpoint", "http://100.90.134.29:8099/mcp"
      ]
    }
  }
}
```

Restart Claude Desktop after saving. Verify via the "+" button in the chat
box → Connectors, which lists connected servers and their tools.

**Claude Code** — project-scoped `.mcp.json` in the repo root:

```json
{
  "mcpServers": {
    "server-assistant": {
      "type": "stdio",
      "command": "/home/noahr/Projects/server-assistant/bin/sa-mcp-shim",
      "args": ["-endpoint", "http://100.90.134.29:8099/mcp"]
    }
  }
}
```

or equivalently from the CLI:

```
claude mcp add --transport stdio server-assistant \
  -- /home/noahr/Projects/server-assistant/bin/sa-mcp-shim -endpoint http://100.90.134.29:8099/mcp
```

Both forms work with `SA_MCP_ENDPOINT` in `env` instead of `-endpoint` in
`args`, if you prefer configuring it that way:

```json
{
  "mcpServers": {
    "server-assistant": {
      "command": "/home/noahr/Projects/server-assistant/bin/sa-mcp-shim",
      "env": { "SA_MCP_ENDPOINT": "http://100.90.134.29:8099/mcp" }
    }
  }
}
```

### 3. What the shim does and doesn't do

- Reads newline-delimited JSON-RPC from stdin, POSTs each message verbatim to
  `-endpoint`/`SA_MCP_ENDPOINT` (default `http://100.90.134.29:8099/mcp`),
  writes the HTTP response back to stdout as one line. No business logic —
  every real answer still comes from the server.
- Never writes anything but protocol JSON to stdout; every log line goes to
  stderr, so a client's stdio session is never corrupted by a stray byte.
- Requires your machine to already be able to reach the endpoint — join the
  tailnet (or be on the same LAN as the Unraid box) first. It opens no
  inbound port on your machine and never touches Funnel.

## Option B — Tailscale Funnel (public HTTPS, not built by this ticket)

Once a human enables Funnel on the target node (`tailscale funnel <port>` —
see `docs/research/mcp-reachability.md` §3 for the exact command and the
already-granted ACL capability), the endpoint becomes a public HTTPS URL
(`https://rijkaardserver.tail8c2c85.ts.net/mcp` on whichever port is
chosen) that any Streamable-HTTP-capable client, including `claude.ai` and
Claude mobile, can register directly with no local process required.

**This is a real trade, not a free upgrade:** Funnel authenticates the
*transport* (valid TLS cert, no port-forward), not the *caller*. Anyone with
the URL — not just the tailnet — can call every read tool on this endpoint.
Only enable it if you accept that public-exposure surface; the stdio shim
above is the private alternative for exactly this reason.
