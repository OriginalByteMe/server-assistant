# Deploy: Docker on the Unraid Host

This is the production deployment path (issue #52): Server Assistant runs
directly on `rijkaardserver` as a Docker container, distributed via
Community Applications. This is a different target from `docs/DEPLOY.md`,
which covers the Mini Lab systemd/LXC demo box (`sa-dev`) — that doc's
"no container required" framing is specific to that demo deployment, not a
statement about this one.

## Topology

- **Host:** `rijkaardserver`, Unraid 7.3.2, LAN `192.168.68.57`, tailnet
  `100.90.134.29` (`rijkaardserver.tail8c2c85.ts.net`).
- **Container:** built from `deploy/docker/Dockerfile`, run via
  `deploy/docker/docker-compose.yml`.
- **Host-facing port: 8099**, not 8080 — 8080 is already bound on this host
  by the unrelated `binhex-qbittorrentvpn` container
  (`docs/research/mcp-reachability.md` §2). The container's own listener
  stays at its untouched default (`:8080`); only the host publish moves.
- **State sources:** `unraid-api`'s GraphQL (primary, over the LAN — see
  below) and a read-only mount of `/var/local/emhttp` (INI fallback).

## Prerequisites

1. Docker and the Compose plugin are already present on Unraid 7.x
   (`docker compose version` on the host).
2. This repository checked out somewhere you can build from (for
   `scripts/deploy-unraid.sh`), or a machine that already has the image.
3. The two human-approved steps below completed.

## Step 1 — create the API key (human-approved, run on the host)

Server Assistant reads Unraid state through `unraid-api`'s GraphQL endpoint,
which requires an authenticated API key — introspection is open but every
real field is gated behind CSRF + session/key auth
(`docs/research/unraid-state-sources.md` §"GraphQL reachability"). No key
exists on the host yet; creating one is a host mutation, so a human runs it,
not this repo's tooling:

```sh
ssh rijkaardserver unraid-api apikey --create --name server-assistant --roles VIEWER
```

This prints the key once — copy it immediately, it is not retrievable
later. Roles are one of `ADMIN, CONNECT, GUEST, VIEWER`
(`unraid-api apikey --help` and the GraphQL `Role` enum agree); `VIEWER` is
the least-privileged read role and the one this deployment is built around.

**If `VIEWER` turns out not to expose a field Server Assistant needs:** the
research pass could not confirm `VIEWER`'s exact grant end-to-end (creating
a key was itself the mutation reserved for a human —
`docs/research/unraid-state-sources.md` §"The permission map"). The
reasoned (not verified) minimum a read-only consumer needs, inferred from
the `Resource`/`Action` enums, is `READ_ANY` (or `READ_OWN`, granularity
unconfirmed) on `DISK, ARRAY, DOCKER, INFO, NETWORK, VARS, SHARE, DISPLAY`
(plus `UPS` if UPS telemetry is wanted). The `unraid-api apikey --create`
CLI only accepts the four coarse roles above, not custom resource:action
grants — if `VIEWER` is confirmed insufficient once this key is live, the
GraphQL schema exposes `Query.getPermissionsForRoles` /
`Query.apiKeyPossiblePermissions` to check a role's actual grant before
escalating to a broader role.

Put the key in `deploy/docker/.env` on the host (create this file by hand —
it is `.gitignore`d and this repo's tooling never writes or ships it):

```sh
ssh rijkaardserver "mkdir -p /mnt/user/appdata/server-assistant && cat > /mnt/user/appdata/server-assistant/.env" <<'EOF'
UNRAID_API_KEY=<paste the key from the apikey --create output>
EOF
ssh rijkaardserver chmod 600 /mnt/user/appdata/server-assistant/.env
```

## Step 2 — deploy the stack

From a checkout of this repo (not on the Unraid host):

```sh
scripts/deploy-unraid.sh --yes-deploy
```

Without `--yes-deploy` (or `CONFIRM_DEPLOY=1`), the script only builds the
image locally and prints what it would do — it never touches the host. With
the flag, it builds, `docker save | ssh ... docker load`s the image,
ships `docker-compose.yml` + `config.docker.yaml` to
`/mnt/user/appdata/server-assistant/` (never `.env` — that stays
hand-provisioned per Step 1), refuses to continue if `.env` is missing on
the host, then runs `docker compose up -d --remove-orphans` and polls for
a healthy container. It touches nothing on the host besides that one
directory and the `server-assistant` container/volume — no other
container, share, or array setting.

Before editing `deploy/docker/config.docker.yaml`, confirm `unraid.graphql_url`
points at the host's real LAN IP (`http://192.168.68.57/graphql`), not
`127.0.0.1` — the container runs in bridge network mode (deliberately, to
keep the 8099 host remap working), so `127.0.0.1` inside the container is
the *container's* loopback, not the Unraid host's.

## Step 3 (optional) — expose the MCP endpoint publicly via Tailscale Funnel

Not required for LAN/tailnet-only use (the dashboard and `/mcp` are already
reachable at `http://192.168.68.57:8099` and
`http://rijkaardserver.tail8c2c85.ts.net:8099` / `http://100.90.134.29:8099`
once Step 2 is up). Funnel is only needed for `claude.ai` web/mobile clients
that can't be pointed at a LAN/tailnet address. This node already has the
Funnel capability granted (no ACL edit needed) and HTTPS certs enabled
(`docs/research/mcp-reachability.md` §1); the one command a human runs on
the host:

```sh
ssh rijkaardserver tailscale funnel --bg --https=443 8099
```

(`8443` and `10000` are the other two ACL-permitted Funnel ports; `8443` is
already used by an unrelated `tailscale serve` mapping for `tdarr-safe`, so
`443` is the correct pick here — use `10000` only if `443` is ever wanted
for something else.) Resulting public URL:
`https://rijkaardserver.tail8c2c85.ts.net`.

**Read before running this:** Funnel authenticates the transport (valid
TLS, no port-forward hole), not the caller. Everything the dashboard serves
on port 8099 — including the read surface (diagnostics, script proposals,
approval history), not just the MCP tool-call path — becomes reachable by
anyone on the public internet who has or guesses the URL, because dashboard
human auth is deferred (issue #51, "Not yet specified"). This is the
concrete trade a human accepts by running the command above; don't run it
before that trade is accepted for this specific deployment.

## Verify

```sh
ssh rijkaardserver "cd /mnt/user/appdata/server-assistant && docker compose ps"
curl -fsS http://192.168.68.57:8099/api/health
```

`docker compose ps` should report the `server-assistant` service `Up
(healthy)` — the healthcheck hits `/api/health` inside the container every
30s. `curl` against the published host port should return the same JSON
`/api/health` returns for the systemd deployment (`{"mode":..., ...}`).

## Roll back

```sh
ssh rijkaardserver "cd /mnt/user/appdata/server-assistant && docker compose down"
```

This stops and removes the container but leaves the named
`server-assistant-data` volume (SQLite state + audit trail) and `.env`
intact — re-running `scripts/deploy-unraid.sh --yes-deploy` restores the
previous state. To roll back to a specific prior image instead of the
latest build, `docker load` the older tag on the host and edit
`docker-compose.yml`'s `image:` line before `docker compose up -d`.

To remove state as well (full reset, rarely what you want):

```sh
ssh rijkaardserver "cd /mnt/user/appdata/server-assistant && docker compose down -v"
```

## Known gap

Raw SMART attributes (`smartctl -n standby -A -j`) need direct device
access. This deployment's container mounts only `/var/local/emhttp` and the
Docker socket (see `docker-compose.yml`'s socket-honesty comment) — no
`/dev` passthrough, no `--privileged`. SMART reads issued from inside this
container will fail closed rather than fabricate a value (CONVENTIONS rule
5, "the observer never lies"). Device access for SMART from this
containerized shape is an open item for a future pass, not something this
packaging ticket adds `--privileged`/device passthrough for.
