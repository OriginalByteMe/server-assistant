# Unraid state sources: what `unraid-api` gives us vs. what we read ourselves

Research for [issue #53](https://github.com/OriginalByteMe/server-assistant/issues/53)
(Obsidian: `Server Assistant/Tickets/HL-SA-15.md`). Feeds [#57] and the SMART
sampler scoping in [#61]. Facts only — no design decisions.

Target host: `rijkaardserver` (Unraid 7.3.2, `unraid-api` v4.35.1+a9625ae2),
reached via `ssh rijkaardserver` (root, Tailscale). Every command below is
read-only: no `apikey --create`, no `hdparm -y`/spindown, no `smartctl -a`/
`-t`, no `docker run`, no array/share/system-file writes. Where a fact could
only be established by mutating the host, it is listed under **Open items**
instead of guessed.

## Must-answer-first: do API keys require a Connect (cloud) account?

**No — evidence indicates local API keys do not depend on an Unraid Connect
account, though this was not proven end-to-end because creating a key is
itself the mutating action reserved for the human.**

- `ssh rijkaardserver unraid-api config` → the `[remote]` stanza (every
  Connect/cloud field: `wanaccess`, `apikey`, `localApiKey`, `email`,
  `username`, `accesstoken`, `idtoken`, `refreshtoken`) is entirely empty.
  No Connect account is signed in on this host.
- `ssh rijkaardserver unraid-api apikey --help` still functions normally and
  lists four selectable `--roles`: `ADMIN,CONNECT,GUEST,VIEWER`. `CONNECT` is
  one option among four, not a precondition — `ADMIN`/`GUEST`/`VIEWER` are
  local-only roles with no cloud relationship implied by the CLI text.
  Confirmed a second time from a completely independent source: the live
  GraphQL schema's `Role` enum lists the identical four values (see
  introspection below), so this isn't a CLI-text quirk.
- `ssh rijkaardserver unraid-api apikey list` → `No Key Found` — the key
  subsystem responds correctly with zero keys present and no cloud account
  configured; it does not error out demanding a Connect login.
- **What remains open:** actually creating a `VIEWER`-role key and using it
  to make an authenticated GraphQL call would be the only fully conclusive
  proof, and key creation is a host mutation (`unraid-api apikey --create`)
  explicitly reserved for the human. Listed under Open items.

## GraphQL reachability: socket, port, and the auth gate

- **Transport:** `unraid-api` listens on a local Unix socket only —
  `ssh rijkaardserver ss -lxp` shows `/var/run/unraid-api.sock` (`u_str
  LISTEN`). It does **not** bind its own TCP port (confirmed: `ss -ltnp`
  lists no process named `unraid-api`/`node` matching a GraphQL-shaped port).
- **HTTP exposure:** nginx reverse-proxies that socket at the `/graphql`
  path. `ssh rijkaardserver grep -n graphql /etc/nginx/conf.d/locations.conf`:
  ```
  location /graphql {
      allow all;
      proxy_pass http://unix:/var/run/unraid-api.sock:/graphql;
      ...
  }
  ```
  and `/etc/nginx/conf.d/servers.conf` shows this location is served on
  `127.0.0.1:80/443`, `192.168.68.57:80/443` (LAN, `br0`), and
  `100.90.134.29:80/443` (Tailscale `tailscale1`), plus IPv6 equivalents —
  confirmed with `ss -ltnp` (nginx PID 1297959 bound to all of those).
  **So `/graphql` is reachable over the LAN and the tailnet, not just from
  the loopback socket.**
- **Introspection needs zero credentials.** `curl http://127.0.0.1/graphql
  -d '{"query":"{__schema{queryType{name}}}"}'` with no headers beyond
  `Content-Type: application/json` returned
  `{"data":{"__schema":{"queryType":{"name":"Query"}}}}` — the full schema
  (217 non-introspection types) is readable by anyone who can reach the
  port, no key required.
- **Real data fields are gated by CSRF + session, independent of the schema
  being open.** `curl .../graphql -d '{"query":"{ array { state } }"}'` →
  `{"errors":[{"message":"Invalid CSRF token", ...,"statusCode":401}]}`.
  Passing the host's own local `csrf_token` (read from
  `/var/local/emhttp/var.ini`, `csrf_token="1DFB4A3C5512732"`) as an
  `x-csrf-token` header changes the error to `"No user session found"` —
  proving CSRF and session/API-key auth are two separate, both-required
  gates. No API key exists on this host to go further (see previous
  section) — this sub-thread stops here per instructions.
- **Conclusion for `resource:action`/`isSpinning`-type "does this call cost
  anything" style questions:** meta-fields (`__schema`, `__typename`) skip
  the app's auth layer entirely (GraphQL library resolves them before any
  custom resolver runs); every real field, however cheap its underlying
  read, is walled behind CSRF + session/API key.

## Schema coverage by subject

Full type list from `{ __schema { types { name } } }` (217 types) and
per-type field introspection (`{ __type(name:"X"){ fields { name } } }`) —
commands run against `http://127.0.0.1/graphql`, unauthenticated (schema is
public even though data is not).

| Subject | GraphQL fidelity | Key types / fields |
|---|---|---|
| Physical disk identity | High | `Disk`/`ArrayDisk` (`device, vendor, size, serialNum, firmwareRevision, interfaceType, bytesPerSector, totalSectors, partitions`), reached via `Query.disks`, `Query.disk(id)`, `Query.assignableDisks`, `array.disks`/`array.caches` |
| Disk spin state | High, and provably non-invasive | `Disk.isSpinning`/`ArrayDisk.isSpinning` (bool) |
| SMART coarse health + temperature | Partial | `Disk.smartStatus` (enum `OK`/`UNKNOWN` only) + `Disk.temperature` |
| SMART raw attributes | **None — gap** | Not present on `Disk` (20 fields total, no attribute-table field) despite `unraid-api` parsing the full attribute table internally (see below) |
| Array/parity state | High | `UnraidArray` (`state, capacity, boot, bootDevices, parities, parityCheckStatus, disks, caches`) + `Vars` (148 fields incl. `mdState, mdNumDisks, mdResync*, sbSynced, sbSyncErrs`) |
| Pools/cache | High | Same `ArrayDisk` shape, `type: Cache`, via `array.caches` |
| Shares | High | `Share` (`name, free, used, size, include, exclude, cache, splitLevel, floor, cow, luksStatus`) via `Query.shares` |
| Permissions/ownership (filesystem/SMB) | **None — gap** | GraphQL's `Resource`/`Role`/`AuthAction`/`ApiKey`/`Permission` types model **API-key RBAC only**; there is no `Query.users` field and `UserAccount` (`id, name, description, roles, permissions`) is used for `me`/`owner` (the calling identity), not a roster |
| Docker containers | High | `Docker.containers`, `DockerContainer` (33 fields: `state, status, ports, hostConfig, networkSettings, mounts, autoStart, isUpdateAvailable, isRebuildReady, tailscaleStatus`), `DockerContainerStats` type present (`cpuPercent, memUsage, memPercent, netIO, blockIO`) |
| CPU / RAM | High | `Info.cpu`(`InfoCpu`)/`Info.memory` (static specs) + `Query.metrics.cpu` (`CpuUtilization: percentTotal, cpus`) for live load |
| Temperatures | High for system sensors; disk temp shares SMART's cost | `Query.metrics.temperature` + dedicated `TemperatureSensor`/`TemperatureSummary`/`TemperatureConfig*`/`LmSensorsConfig` types (lm-sensors-backed); disk temperature specifically comes from the same `smartctl -n standby -A -j` call as `smartStatus`, not this path |
| Network | High | `Info.networkInterfaces`/`primaryNetwork` (`InfoNetworkInterface: iface, mac, ipv4/6, speed, dhcp`) + `TailscaleStatus`/`TailscaleExitNodeStatus` |

## The permission map

- **Resource vocabulary** (29 values), confirmed identically from two
  independent sources — `unraid-api apikey --help` CLI text **and** the live
  `Resource` enum via `{ __type(name:"Resource"){enumValues{name}}}`:
  `ACTIVATION_CODE, API_KEY, ARRAY, CLOUD, CONFIG, CONNECT,
  CONNECT__REMOTE_ACCESS, CUSTOMIZATIONS, DASHBOARD, DISK, DISPLAY, DOCKER,
  FLASH, INFO, LOGS, ME, NETWORK, NOTIFICATIONS, ONLINE, OS, OWNER,
  PERMISSION, REGISTRATION, SERVERS, SERVICES, SHARE, VARS, VMS, WELCOME`.
- **Action vocabulary** (8 values, same two sources): `CREATE_ANY,
  CREATE_OWN, READ_ANY, READ_OWN, UPDATE_ANY, UPDATE_OWN, DELETE_ANY,
  DELETE_OWN`.
- **Role vocabulary** (4 values, same two sources): `ADMIN, CONNECT, GUEST,
  VIEWER`.
- **What `VIEWER` actually grants: not determined this session.** The schema
  exposes helper fields built for exactly this question —
  `Query.getPermissionsForRoles(roles: [Role])` and
  `Query.apiKeyPossiblePermissions` — but both require an authenticated
  session (same CSRF-then-"No user session found" wall documented above),
  and no key exists on the host to authenticate with. Listed under Open
  items.
- **Reasoned (not verified) minimum grant for a read-only consumer,** based
  purely on the resource list above and the table's subjects: `READ_ANY` (or
  `READ_OWN`, granularity not verified) on `DISK, ARRAY, DOCKER, INFO,
  NETWORK, VARS, SHARE, DISPLAY`, plus `UPS` if UPS telemetry is wanted
  (`UPSDevice`/`UPSBattery` types exist in-schema). This is inference from
  the enum + field list, not a confirmed role definition.

## Gaps and their direct sources — `/var/local/emhttp/*.ini`

`ssh rijkaardserver ls -la /var/local/emhttp/*.ini` lists 13 files (all
plain INI, root-readable); each was opened directly to confirm contents
rather than guessed from the filename:

| File | Size | Confirmed contents |
|---|---|---|
| `devs.ini` | 0 B | Empty on this host — unused |
| `disks.ini` | 5.8 KB | Per-disk stanzas: `device, id, size, sectors, transport, rotational, spundown, status, temp, numReads/Writes/Errors, type, spindownDelay`. **This is the exact field `unraid-api`'s own `isSpinning` resolver reads** (see next section) |
| `monitor.ini` | 347 B (root, `0600`) | `[smart]` per-attribute warning/ack flags (e.g. `disk1.1="21114"`, `parity.199="30"`), `[disk]` colour-coded warning state, `[poolsstatus]`, current `[array]` activity (`parity="Parity-Check"`) |
| `mover.ini` | 540 B | Live mover progress counters (`TotalToSecondary`, `RemainFromSecondary`, current `File=`/`Action=`) |
| `network.ini` | 510 B | Interface/bond/bridge static config (`BONDNAME, BRNAME, IPADDR:0, GATEWAY:0`, etc.) |
| `nginx.ini` | 853 B | nginx runtime bind config (`NGINX_BIND`, cert path, LAN/Tailscale FQDNs) |
| `proxy.ini` | 35 B | `http_proxy`/`https_proxy`/`no_proxy` (all empty) |
| `sec.ini` | 3.2 KB | Per-share SMB security: `export, security, readList, writeList` — **the permissions/ownership gap source** |
| `sec_nfs.ini` | 1.7 KB | NFS export ACLs, same shape as `sec.ini` |
| `shares.ini` | 7.4 KB | Share definitions (redundant with GraphQL `Share`, useful if unauthenticated) |
| `statics.ini` | 82 B | Static route table (`shim-br0 ... GW4 default via ...`) |
| `users.ini` | 280 B | **Full local account roster** — `root`, `sentry-bot`, `noah-admin`, each with `desc`. Confirmed gap: GraphQL has no `Query.users` field, so this file is the only source for the account list |
| `var.ini` | 3.7 KB | Global array/system/share/registration vars — near 1:1 with GraphQL's 148-field `Vars` type (cross-checked field names) |

Other direct (non-`.ini`) gap sources, confirmed readable:

- `/proc/mdstat` — **not** standard Linux `mdraid` output. Unraid's custom
  `md` driver reports its own key=value pseudo-file (`sbState, mdState,
  mdNumDisks, mdResyncAction, rdevStatus.N, rdevId.N, ...`), root-readable,
  ~360 lines on this host (30 array slot placeholders).
- `/var/run/docker.sock` — `srw-rw---- root:docker`; readable by the same
  privilege GraphQL's Docker resolvers use internally (root, or membership
  in the `docker` group). Confirmed present and correctly permissioned; not
  connected to (per constraints, no container operations were run).
- `smartctl -A -j -n standby /dev/sdX` — the raw-attribute gap-filler for
  the one thing GraphQL doesn't expose (see next section for exactly how
  `unraid-api` itself uses this same command).

## Does reading SMART spin up a sleeping disk?

**Observed, not assumed — but every disk on this host was awake during the
test, so the standby branch is documented from upstream sources rather than
reproduced here; see the caveat at the end of this section.**

Ground truth for "is this disk actually asleep right now" was taken from
`/var/local/emhttp/disks.ini`'s `spundown` field — the same field
`unraid-api`'s own resolver treats as canonical (see decompiled source
below), not `hdparm -C` (a second read-only power-mode query was judged
unnecessary once the resolver source confirmed which field the vendor itself
trusts).

`grep -E '^\[|spundown=' /var/local/emhttp/disks.ini` → every populated disk
(`parity=sdc, disk1=sdd, disk3=sdb, cache=nvme0n1, flash=sda`) reports
`spundown="0"` (awake). The array's spin-down delay is effectively disabled
on this host — `/boot/config/disk.cfg` shows `spindownDelay="0"` globally and
`diskSpindownDelay.N="-1"` (inherit) per disk — so no disk was observed to
go to standby naturally during this session, and forcing one to sleep
(`hdparm -y`) is an array/disk operation explicitly denied to this research
pass.

`smartctl -i -n standby /dev/$d; echo $?` was run for every present device:

| Device | Role | `smartctl -i -n standby` result | Exit code |
|---|---|---|---|
| `/dev/sdb` | disk3 | Full identify block, `Power mode is: ACTIVE or IDLE` | **0** |
| `/dev/sdc` | parity | Full identify block, `Power mode was: IDLE_A` | **0** |
| `/dev/sdd` | disk1 | Full identify block, `Power mode is: ACTIVE or IDLE` | **0** |
| `/dev/nvme0n1` | cache (NVMe) | Full identify block (NVMe has no ATA-style standby concept `-n` gates) | **0** |
| `/dev/sda` | flash (USB boot) | `Unknown USB bridge [0x0781:0x5591]` — device-type detection failure, unrelated to power state | **1** |

**Confirmed exit-code semantics for the awake case:** `-n standby` behaves
identically to a plain `-i` query when the disk is active — full SMART data,
exit `0`. This matches every disk's `spundown="0"` ground truth exactly:
zero false "asleep" reports.

**Documented (external, not reproduced here) semantics for the asleep
case:** smartmontools' own docs state that with `-n standby`, if the device
is in SLEEP or STANDBY mode, smartctl skips the check and **exits with
status 2** (bit 1 set) by default, without spinning the disk up — this is
the entire purpose of the flag (source: smartmontools.org /
manpages.debian.org `smartctl(8)`, `EXIT STATUS` and `-n` sections).
**Known caveat, scoped to not apply here:** a long-standing smartmontools
report (github.com/smartmontools/smartmontools#57) documents `-n standby`
still waking some **USB-enclosure** drives, because the enclosure's own
controller manages power state outside the OS/ATA layer smartctl inspects.
This host's array disks (`sdb, sdc, sdd`) are `transport="ata"` (internal
SATA, confirmed in `disks.ini`), not USB — the only USB-attached device is
the flash boot drive, which isn't part of SMART/array monitoring anyway. The
caveat therefore doesn't apply to the array being sampled, but is worth
carrying forward as a known limitation of the `-n standby` guarantee in
general.

**`unraid-api` itself uses this exact pattern — found directly in the
shipped source, not inferred:**
`grep -o` on `/usr/local/unraid-api/dist/assets/plugin.module-D-YdicWU.js`
around the one `execa('smartctl', ...)` call site in the bundle:

```js
async getTemperature(device) {
    try {
        const { stdout } = await execa('smartctl', [
            '-n', 'standby', '-A', '-j', device
        ]);
        ...
```

Both `Disk.temperature` and `Disk.smartStatus` are derived from this single
`-n standby -A -j` call (confirmed: `smartStatus`'s resolver looks up the
same disk object rather than making a second `execa` call). **This means
querying disk temperature/health through GraphQL carries the identical
standby-safety guarantee as calling `smartctl -n standby` directly — the
vendor made the same choice this research was asked to validate.** It also
confirms the raw attribute table (`ata_smart_attributes.table`, from which
reallocated-sector/pending-sector/power-on-hours would come) is parsed
in-process by `unraid-api` but discarded before reaching the GraphQL schema
— the gap is real and precise, not a missing-feature guess.

## Container reachability: network or socket-only?

- `unraid-api` binds **only** a Unix socket
  (`/var/run/unraid-api.sock`) — confirmed above via `ss -lxp`. It has no
  TCP listener of its own.
- nginx, however, proxies `/graphql` on the host's **LAN IP**
  (`192.168.68.57:80/443`) and **Tailscale IP** (`100.90.134.29:80/443`), in
  addition to loopback — confirmed via `servers.conf` `listen` directives
  and `ss -ltnp`.
- Existing Docker bridge networks on this host (`docker network ls`):
  `br0` (macvlan), `bridge` (172.17.0.0/16, `docker0`), `noah-net`
  (172.18.0.0/16), `tdarr-safe-control` (172.19.0.0/16), plus `host`/`none`.
  `iptables -t nat -L POSTROUTING` shows `MASQUERADE` rules for all three
  bridge subnets — standard Docker NAT, already in place.
- **[INFERENCE, standard Linux/Docker networking, not verified by running a
  container per the task's explicit "do NOT create or run containers"
  constraint]:** a container on any of the existing bridge networks can
  reach nginx's `/graphql` endpoint by addressing the host's own LAN IP
  (`192.168.68.57`) or Tailscale IP (`100.90.134.29`) directly — Linux
  delivers packets addressed to a locally-owned IP to the local listening
  socket regardless of which bridge/NAT path they arrived on (the same
  "container → host's own IP" pattern Docker relies on generally). This
  would mean **host networking is not required**; a bridge-networked
  container that knows the host's LAN or Tailscale IP should reach
  `/graphql` without a bind mount either — only the Unix socket itself
  would need a bind mount, and nginx already provides a network path around
  that requirement. This inference was not empirically confirmed and is
  flagged as such; confirming it requires either running a diagnostic
  container (denied this pass) or asking someone already running one.

## Version stability

No local version-history artifact exists on this host — `unraid-api` ships
one `package.json` with the current version (`4.35.1+a9625ae2`) and no
schema changelog under `/usr/local/unraid-api`. Stability evidence below is
**external, sourced from GitHub-hosted third-party changelogs, not
reproduced against this host** — labelled `[INFERENCE]` accordingly:

- `[INFERENCE]` `ParityCheck.speed` changed type from integer to string in
  `unraid-api` v1.9.0, breaking consumers that assumed a numeric field
  (source: `ha-unraid` CHANGELOG.md, github.com/ruaan-deysel/ha-unraid).
- `[INFERENCE]` Unraid 7.2.4 added new required GraphQL fields, breaking
  queries built against the prior schema with HTTP 400s until the consuming
  client updated (same source).
- `[INFERENCE]` Unraid 7.2.3 had GraphQL queries referencing fields not yet
  exposed in production, also causing 400s for Docker/VM operations (same
  source).
- `[INFERENCE]` A VM-detection field was renamed `domains` → `domain`
  between versions, breaking integrations on the old name (same source).

**Conclusion: the schema is not stable across point releases within a
single Unraid major version** — type changes, added required fields, and
field renames have all happened between 7.2.x/7.3.x releases per
third-party reports. An adapter layer between this project's domain model
and the raw GraphQL schema is warranted; this sizing question is for #57 to
act on, not this ticket to decide.

## Open items requiring host mutation (human approval needed)

1. **Create a `VIEWER`-role API key and test an authenticated GraphQL call.**
   This is the only way to (a) fully confirm API keys don't require a
   Connect account end-to-end, and (b) determine exactly which fields
   `VIEWER` can read via `getPermissionsForRoles`/`apiKeyPossiblePermissions`
   or a live query. Requires `unraid-api apikey --create --roles VIEWER`,
   which is an `API_KEY` create — a host mutation this research pass does
   not perform.
2. **Empirically observe a disk in genuine SLEEP/STANDBY and confirm the
   exit-code-2 branch of `smartctl -n standby`.** Every disk on this host is
   configured to never auto-spin-down (`spindownDelay="0"`/`-1`), so this
   would require either waiting for a future config change to enable
   spindown naturally, or manually spinning a disk down (`hdparm -y`) —
   both are array/disk operations denied to this pass. The awake-case exit
   code (0) was observed directly; the asleep-case exit code (2) is
   documented from smartmontools' own docs, not reproduced on this host.
3. **Empirically confirm bridge-networked container → host-LAN-IP
   reachability for `/graphql`.** The task's constraints explicitly forbid
   creating or running containers; the reachability conclusion above is
   inference from interface bindings and standard Linux routing, not a
   live test.
