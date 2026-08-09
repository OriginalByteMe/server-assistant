# MCP transport reachability and Unraid packaging

Research for HL-SA-20 (Obsidian: `Server Assistant/Tickets/HL-SA-20.md`).
Settles two things read-only, against the live host: what it actually takes to
make the MCP endpoint reachable from Claude's cloud infrastructure via
Tailscale Funnel (issue #56), and what port the dashboard would occupy if
packaged onto `rijkaardserver` itself (issue #51 — "runs on the Unraid host").

No ACL, Funnel, `serve`, or other network state was changed to produce this
document. Every command below is a read-only subcommand or `--help`; the two
mutating verbs (`tailscale funnel <target>`, `tailscale serve <target>`) were
never invoked.

## 1. Current Tailscale state on `rijkaardserver`

Command: `ssh rijkaardserver tailscale status --json`

| Fact | Value | Field |
|---|---|---|
| Node DNS name | `rijkaardserver.tail8c2c85.ts.net` | `.Self.DNSName` |
| Tailnet | `noahrijkaard@gmail.com` | `.CurrentTailnet.Name` |
| MagicDNS suffix | `tail8c2c85.ts.net` | `.MagicDNSSuffix` |
| Tailscale IP | `100.90.134.29` | `.Self.TailscaleIPs[0]` |
| Backend state | `Running`, `Online: true` | `.BackendState`, `.Self.Online` |
| Tailscale version | `1.98.9` | `tailscale version` |

**HTTPS certs are enabled for this tailnet.** `.CertDomains` is
`["rijkaardserver.tail8c2c85.ts.net"]` — non-empty, which is the documented
signal that HTTPS certificate issuance is turned on for the tailnet and this
node has a resolvable cert domain. (Both Funnel and `serve --https` require
this; an empty `CertDomains` array would mean HTTPS certs are off tailnet-wide
and neither would work regardless of ACL.)

**The Funnel node attribute is already granted to this node.** `.Self.Capabilities`
includes `"funnel"`, `"https"`, and
`"https://tailscale.com/cap/funnel-ports?ports=443,8443,10000"`. This is the
node's live, control-plane-resolved capability set — exactly what determines
whether `tailscale funnel` would succeed — and it is populated from the
tailnet's ACL policy (`nodeAttrs` funnel grant) without needing to read the
ACL text itself. **Conclusion: no ACL edit is required to enable Funnel for
this node.** The grant is scoped to three incoming ports only: **443, 8443,
10000**.

(For completeness: the raw ACL policy JSON itself lives in the Tailscale admin
console / API, not on the node, so it wasn't fetched directly — the resolved
capability set above is the authoritative, node-local read of the same fact
and is what the `tailscale funnel`/`serve` commands themselves check.)

Command: `ssh rijkaardserver tailscale funnel status` (also cross-checked with
`tailscale funnel status --json` and `tailscale serve status`, which return
the same underlying config — `serve` and `funnel` share one config store):

```
https://rijkaardserver.tail8c2c85.ts.net:8443 (tailnet only)
|-- / proxy http://127.0.0.1:18265
```

**Funnel is not currently active.** There is one existing `tailscale serve`
mapping already configured on this node — HTTPS on port **8443**, proxying to
a local backend on `127.0.0.1:18265` — but it is explicitly annotated
`(tailnet only)`. The JSON form (`tailscale funnel status --json`) confirms
this: the `TCP`/`Web` config exists but carries no public/`AllowFunnel` flag.
`docker ps` identifies the backend on port 18265 as the `tdarr-safe`
container — this mapping predates and is unrelated to Server Assistant; it
just means **port 8443 is already spoken for** by another service's tailnet-only
serve config, out of the three ports (443, 8443, 10000) this node is permitted
to Funnel on.

## 2. Port collision check for the dashboard

Command (this repo): `grep -rn 'http_addr\|HTTPAddr' internal/config/ deploy/ *.yaml`

The dashboard's HTTP listener defaults to `:8080` (`internal/config/config.go`
resolves `c.HTTPAddr = ":8080"` when unset; `config.example.yaml` and
`deploy/config.demo.yaml` both pin `http_addr: "0.0.0.0:8080"` explicitly; the
current `sa-dev` LXC demo also runs on 8080 per `docs/DEMO.md`).

Command: `ssh rijkaardserver ss -ltnp` — port 8080 is **already bound** on
`rijkaardserver` itself:

```
LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:* users:(("docker-proxy",pid=353520,fd=8))
LISTEN 0 4096    [::]:8080    [::]:* users:(("docker-proxy",pid=353527,fd=8))
```

Command: `ssh rijkaardserver "docker ps --format '{{.Names}} {{.Ports}}' | grep ':8080->'"`
identifies the owner: **`binhex-qbittorrentvpn`** (`0.0.0.0:8080->8080/tcp`).

**Conclusion: deploying Server Assistant directly on `rijkaardserver` with its
default `http_addr: :8080` collides with an existing, unrelated container.**
The Unraid-host deployment's config must override `http_addr` to an unused
local port (any port not in the `ss -ltnp` listing above — the Funnel/serve
target port is independent of the port a public hostname answers on, so this
is a config-file change on install, not a product blocker).

## 3. Required human-approved change-set to enable Funnel

Because the ACL already grants this node the `funnel` attribute (§1) and
HTTPS certs are on, the change-set a human has to approve is **one command**,
not an ACL edit:

1. **Pick an unused Funnel port.** Of the three ACL-permitted ports
   (443, 8443, 10000), 8443 is taken by the pre-existing `tdarr-safe` mapping
   (§1). Use **443** (the standard, no-port-in-URL choice) or **10000** if 443
   is ever wanted for something else.
2. **Pick the dashboard's actual local bind port** — the non-colliding port
   chosen in §2 (e.g. `:8090`), not `:8080`.
3. **The one command**, run on `rijkaardserver` by a human with SSH/console
   access to the box (syntax from `tailscale funnel --help`):

   ```
   tailscale funnel --bg --https=443 8090
   ```

   (`--https=443` selects the incoming Funnel port; `8090` is the dashboard's
   local port; `--bg` backgrounds it so it survives the SSH session.)

4. **Resulting public hostname:** `https://rijkaardserver.tail8c2c85.ts.net`
   (or `:10000` if that port is chosen instead) — a real Tailscale-issued
   certificate, no port-forward, no public DNS record needed.

**What this exposes, explicitly:** everything the dashboard serves on that
local port becomes reachable by anyone on the public internet who has or
guesses the URL — Tailscale Funnel authenticates the *transport* (valid TLS,
no port-forward hole) but does **not** authenticate the *caller*. It does not
gate access to the MCP tool surface or the dashboard UI. Per issue #51's
settled safety model, this is currently mitigated only by:
- **The approval gate**: no mutating action executes without a human clicking
  Approve in the dashboard, regardless of who reached the MCP endpoint.
- **Hash-bound, argument-free scripts**: an approved script is bound to its
  exact content hash; nothing an anonymous caller sends can change what a
  script does or supply parameters to it.
- **The explicit unauthenticated-dashboard warning** (issue #51, "Not yet
  specified") — dashboard human auth is deferred by Noah's hold, so until it
  lands, publishing via Funnel means the *read* surface (diagnostics, script
  proposals, approval history) is also publicly exposed, not just the MCP
  tool-call path. This is the concrete risk a human approves when running the
  command above — it should not be run before that trade is accepted for the
  specific deployment.

Running this command, and the ACL edit it would need if the node did *not*
already have the grant (`tailscale.com` admin console → Access Controls →
add a `nodeAttrs` entry with `"attr": ["funnel"]` for the node/tag), are both
squarely in the DENIED category for this research pass and were not executed.

## 4. stdio-shim fallback, concretely

The fallback from issue #56: a small process that runs **on the user's own
machine** (not on Unraid), speaks MCP's stdio transport to a local client, and
relays each request to the real MCP endpoint on the Unraid box.

- **What it does:** a stdio↔Streamable-HTTP bridge. It reads MCP JSON-RPC
  frames on stdin/stdout (the shape Claude Desktop's local server config
  expects) and forwards each request as an HTTP POST to the dashboard's
  `/mcp` endpoint, returning the response verbatim. No local business logic —
  the shim is dumb by design, all behavior stays server-side.
- **How it reaches the Unraid box:** whatever network the user's machine
  already has — LAN (`http://192.168.68.57:<dashboard-port>/mcp`) if on the
  same network, or the tailnet (`http://rijkaardserver.tail8c2c85.ts.net:<port>/mcp`
  or the raw tailnet IP `100.90.134.29:<port>`) if the user's own device is
  tailnet-joined but not exposing anything publicly. Either way it is a plain
  outbound HTTP call the shim makes for the user — no inbound port opens on
  their machine, and Funnel is never involved.
- **Which clients it serves:** only clients that can spawn and own a local
  process talking stdio — i.e. **Claude Desktop's local MCP server config**
  (a `command`/`args` entry in its config file). It does **not** work for
  `claude.ai` (web) or the Claude mobile app, both of which only speak to
  remote HTTP MCP servers from Anthropic's own infrastructure — matching
  issue #56's resolution exactly.
- **Minimal-dependency build note:** this is a ~40-line stdio-transport
  wrapper, not a new product surface. It should reuse an existing MCP SDK's
  stdio transport (Go or TypeScript) rather than hand-roll JSON-RPC framing;
  no new runtime dependency is needed in the Server Assistant binary itself
  since the shim ships and runs separately, on the user's machine.

## 5. Dashboard reachability self-check — design

Issue #56 (Noah's addition) and issue #51 both require the dashboard to
*observe*, not assume, which of four states the MCP endpoint is in. This
mirrors ADR 0005 ("the observer never lies" — no path may collapse "can't
tell" into a wrong answer), so each state below is tested independently
rather than inferred from a single flag.

Recommended mechanism: the dashboard process, running on the Unraid host
itself, shells out to the local `tailscale` CLI (`os/exec`, no new Go
dependency — consistent with CONVENTIONS' stdlib-first rule) rather than
linking `tailscale.com/client/tailscale`, since the CLI's `--json` output
already carries every field needed and adding a client library would need the
same "why stdlib won't do" justification the conventions table requires for
every new dependency.

| # | State | Distinguishing signal |
|---|---|---|
| 1 | **Tailscale absent** | `exec.Command("tailscale", "status", "--json")` fails to start (binary not found) *or* returns a connection error to the daemon (`tailscaled` not running / no local socket). This is checked first and short-circuits the rest — if there's no Tailscale, states 2–4 are meaningless. |
| 2 | **Tailnet-only** | `tailscale status --json` succeeds (`BackendState == "Running"`) **and** `tailscale serve status --json` shows a `Web`/`TCP` entry for the dashboard's own configured port, but that entry's Funnel flag is absent — the exact shape seen live in §1 (`(tailnet only)` in the plain-text form; no `AllowFunnel`-equivalent true in the JSON form). MCP is reachable from the tailnet; Claude's cloud client cannot reach it. |
| 3 | **Funnel serving publicly** | Same `serve status --json` query, but the entry for the dashboard's own port *does* carry the public/Funnel flag set. The dashboard reads the actual hostname straight out of that live config (the `Web` map's key) rather than a static setting it remembers configuring — so it can never claim "public" after a human has since run `tailscale funnel reset`, and never claim "tailnet-only" after Funnel was enabled out-of-band. Shown to the human with an explicit "this is publicly reachable" note (issue #56 requirement). |
| 4 | **Endpoint configured but failing** | Independent of states 2/3: whichever `serve`/`funnel` config exists for the dashboard's port names a local proxy target (`http://127.0.0.1:<port>`, per §1's actual mapping). The self-check separately dials **that exact target** with a short-timeout HTTP probe (matching the existing `prober.NewReachability`/`NewHTTP` pattern already in this codebase — `internal/prober`). If Tailscale's config says traffic is routed but the local probe fails or times out, that is reported as its own distinct state — the fault is in the dashboard process, not in Tailscale or in Anthropic's egress — rather than being silently folded into state 2 or 3. |

States 1–3 are mutually exclusive and checked in order (1 short-circuits; 2
vs. 3 is one boolean read off the live serve config). State 4 is an
orthogonal axis — "is the pipe configured" vs. "does anything answer at the
other end" — and can in principle combine with either 2 or 3 (a tailnet-only
*or* a Funnel-public mapping can each independently be pointed at a dead
backend), so the UI should render it as a qualifier ("configured, not
responding") layered on top of whichever of 2/3 is true, not a fifth
mutually-exclusive bucket.
