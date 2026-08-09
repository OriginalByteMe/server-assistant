# Findings: shimmed dry-run executor prototype

Answers [issue #54](https://github.com/OriginalByteMe/server-assistant/issues/54).
Throwaway prototype — see `README.md`. Every transcript below is pasted
verbatim from a real run captured under `transcripts/`; every script is a
byte-exact copy of a script that runs today on the user's Unraid box
`rijkaardserver` (see `PROVENANCE.md`). Nothing here was executed on that
host — only read (`cat`, `stat` over SSH).

## Architecture, in one paragraph

`run.sh` puts a directory of logging shims (`docker rm mv cp chmod chown
mkfs dd rsync ln tee truncate`) first on `PATH`, then runs the target script
inside a real unprivileged `unshare --user --map-root-user --mount --net`
namespace: the entire filesystem is bind-mounted onto itself and remounted
read-only (`mount --bind / /` then `mount -o remount,bind,ro /`), and the
network namespace has no interface but loopback. `strace -f -e trace=execve`
runs alongside for ground-truth exec tracing. A transcript log (fd 3) and
strace log (fd 4) are opened on the host *before* the namespace exists, so
shims can keep writing evidence out even though the tree they're running
against is read-only. `predicate.sh` then classifies the run APPROVED or
REJECTED. None of the 12 shims ever perform the real operation — they log
argv and return a canned exit code.

## Question 1 — is the transcript more informative than reading the script?

**It depends on the script, and the prototype answers this per-script, not
in general:**

- **Trivial one-liners** (`bittorrent-stalled-torrent-remover`,
  `viewDockerLogSize`) — no. The transcript for
  `bittorrent-stalled-torrent-remover` is one line:
  ```
  [2026-08-09T17:20:55.108594383+0800] 52857 CMD docker start e733a8179c2a
  ```
  Reading the 37-byte script already tells you exactly this. The only thing
  the transcript adds is confirmation that nothing *else* happened.

- **Scripts with real, non-obvious argument construction** — yes,
  materially. `delete_dangling_images` builds its `docker rmi` argument list
  from a command substitution the reader would otherwise have to mentally
  evaluate:
  ```
  [2026-08-09T17:20:54.982696953+0800] 52809 CMD docker images --quiet --filter dangling=true
  [2026-08-09T17:20:54.996678658+0800] 52816 CMD docker rmi sha256:deadbeefcafe0001 sha256:deadbeefcafe0002
  ```
  The transcript shows the *resolved* `docker rmi` argv, not the
  `$(...)`-shaped source. (Caveat below: the specific image IDs are
  fabricated by the docker shim's canned response, not the host's real
  dangling images — see the "deliberate scope decision" and divergence
  section.)

- **Scripts with real branching on live state** — yes, decisively.
  `clamav_weekly_scan` is 13 lines and easy to read, but what it *does* on
  a given run depends entirely on whether a container named `ClamAV` is
  running — information that isn't in the script text at all. See Question 3.

**Verdict:** the transcript is more informative exactly when a script's
real behavior depends on runtime data the reader can't get from the source
alone (command substitutions, `docker ps`/`docker inspect` branches,
computed paths). For scripts that are already a straight line with no
runtime-dependent argument, the transcript adds confirmation, not
information.

## Question 2 — what "cannot complete a dry run" means mechanically

**The exact predicate** (implemented in `predicate.sh`), REJECTED iff **any**
of:

- **(a) Exit status.** The script's own top-level exit code is nonzero, OR
  it did not terminate within the wall-clock timeout (`timeout -k 2 <N>`,
  exit 124). A dry run that never finishes is not evidence of anything.
- **(b) Unshimmed mutation-risk binary invoked.** `strace`'s execve trace
  shows a call to a binary whose resolved path is outside the shim
  directory (`bin/`) **and** whose basename is not on the fixed,
  human-curated `safe-readonly.list` (cat, grep, curl, jq, mountpoint, …).
  This is deliberately an allowlist of known-safe, not a blocklist of
  known-unsafe — see the honest-ceiling section for why.
- **(c) Attempted write outside the sandbox scope.** A shimmed mutating
  call's target path (parsed from argv, resolved with `realpath -m`) falls
  outside the designated scratch scope, or under `/boot` (per issue #51:
  never write `/boot`). The shims log this as a `VIOLATION` line
  independently of the script's own exit code.

Any other outcome is `APPROVED-FOR-REVIEW` — evidence, never proof (issue
#51's framing; the dashboard says "would do", never "will do").

### Demonstration: each branch, tripped by a real script

**(a) — nonzero exit.** `clamav_weekly_scan`, ClamAV canned as not running
(`scenarios/clamav-not-running.env`):
```
=== dry-run: real-scripts/clamav_weekly_scan/script ===
=== scenario: scenarios/clamav-not-running.env ===
[2026-08-09T17:20:55.220698194+0800] 52897 CMD docker ps --format \{\{.Names\}\}
=== script exit code: 1 (124 = killed by timeout after 15s) ===
VERDICT: REJECTED — cannot complete a dry run
  - (a) script exited nonzero: 1
```
The script's own logic (`if ! docker ps ... | grep -q '^ClamAV$'; then ...
exit 1; fi`) is what produced the nonzero exit — the predicate doesn't
special-case it.

**(b) — unshimmed mutation-risk binary.** `tdarr-plex-gate`, run with a
`/mnt/user` fixture mount so its `mountpoint -q /mnt/user` guard passes:
```
=== dry-run: real-scripts/tdarr-plex-gate/script ===
[2026-08-09T17:21:01.512804627+0800] 53089 CMD docker inspect -f \{\{.State.Status\}\} tdarr
=== script exit code: 0 (124 = killed by timeout after 10s) ===
VERDICT: REJECTED — cannot complete a dry run
  - (b) unshimmed binary invoked: /usr/bin/mkdir (not on safe-readonly.list, not a known-mutating shim)
```
The script itself exits 0 (its Plex-unreachable fail-safe path is quiet by
design) — a naive "check the exit code" gate would have **approved** this
script. The predicate catches it anyway because line 13, `mkdir -p
"$STATE_DIR"`, calls a real binary this shim set doesn't cover. `mkdir` was
never in the issue's shim list; this is a genuine gap the prototype exposes,
not a contrived example.

**(c) — write outside the sandbox.** `delete.ds_store`, run with a `/mnt/user`
fixture pointed at a local test tree containing two `.DS_Store` files:
```
=== dry-run: real-scripts/delete.ds_store/script ===
[2026-08-09T17:21:01.325855116+0800] 53018 CMD rm /mnt/user/appdata/somecontainer/.DS_Store
[2026-08-09T17:21:01.338190638+0800] 53018 VIOLATION cmd=rm target=/mnt/user/appdata/somecontainer/.DS_Store reason=outside-sandbox-scope
[2026-08-09T17:21:01.362237635+0800] 53030 CMD rm /mnt/user/Media/Movies/.DS_Store
[2026-08-09T17:21:01.375631221+0800] 53030 VIOLATION cmd=rm target=/mnt/user/Media/Movies/.DS_Store reason=outside-sandbox-scope
=== script exit code: 0 (124 = killed by timeout after 15s) ===
VERDICT: REJECTED — cannot complete a dry run
  - (c) VIOLATION cmd=rm target=/mnt/user/appdata/somecontainer/.DS_Store reason=outside-sandbox-scope
  - (c) VIOLATION cmd=rm target=/mnt/user/Media/Movies/.DS_Store reason=outside-sandbox-scope
```
This prototype's sandbox scope is always empty (see "deliberate scope
decision" below), so **every** real mutating call against a real production
path trips this branch by design — that's the intended behavior for a
harness with no configured allowlist of writable directories yet.

### Bonus branch not in the issue's three, found by evidence: non-termination as a first-class case

`trawl-vpn-guard` is a long-running daemon (`while true; do enforce; timeout
30 docker events ...; done`), not a one-shot maintenance script — designed
to run forever with a real ~30-second cadence. See "runaway loop" below for
why this specific script also demonstrates why (a) needed a timeout clause
at all, not just "nonzero exit".

## Question 3 — control-flow divergence when a script branches on shimmed output

**Demonstrated directly: the same unmodified script, run twice, only the
docker shim's canned answer changed, and the transcripts diverge
completely.**

`clamav_weekly_scan` branches on `docker ps --format '{{.Names}}' | grep -q
'^ClamAV$'`. Run 1 (`scenarios/clamav-not-running.env`,
`DRYRUN_DOCKER_PS_NAMES=""`):
```
[2026-08-09T17:20:55.220698194+0800] 52897 CMD docker ps --format \{\{.Names\}\}
=== script exit code: 1 ===
```
Zero mutating calls attempted. Run 2 (`scenarios/clamav-running.env`,
`DRYRUN_DOCKER_PS_NAMES="ClamAV\nPlex\ntdarr"`):
```
[2026-08-09T17:20:55.332250895+0800] 52940 CMD docker ps --format \{\{.Names\}\}
[2026-08-09T17:20:55.349630255+0800] 52948 CMD docker exec ClamAV clamdscan --multiscan --fdpass --infected --move=/var/lib/clamav/quarantine --log=/var/lib/clamav/scan.log /scandir
=== script exit code: 0 ===
```
Identical bytes on disk, identical invocation — the *only* difference
between these two runs is what the docker shim was told to say `docker ps`
returned. **The transcript's content is a function of the operator's
canned guess about the fleet's live state, not of the fleet's actual live
state.** A script whose real, undisclosed behavior is destructive (say, a
hypothetical `if <condition>; then rm -rf ...; fi`) can show a completely
clean, zero-mutation transcript in a dry run and a destructive one in
production, purely because the dry run's canned `docker ps` didn't happen
to match reality that day. This is exactly the risk issue #51 names when it
says a dry run is evidence, not proof — and this prototype shows *why* in a
concrete, reproducible way rather than asserting it.

## The runaway-loop finding

`trawl-vpn-guard` polls in `while true; do enforce; timeout 30 docker
events ...; done` — designed for roughly one iteration every 30 seconds. A
real `docker events` blocks until something happens; the docker shim
**returns immediately** with canned (empty) output, because a shim that
tried to block for a real 30 seconds every dry run would make the harness
unusable. Run with a `/mnt/user` fixture so the script's `mountpoint -q`
guard passes, `timeout 4` (harness-level, not the script's internal one):

```
[2026-08-09T17:21:07.510083314+0800] 53237 CMD docker inspect -f \{\{.State.Running\}\} trawl
[2026-08-09T17:21:07.525480547+0800] 53244 CMD docker exec binhex-qbittorrentvpn ip link show wg0
[2026-08-09T17:21:07.542351218+0800] 53253 CMD docker exec binhex-qbittorrentvpn ip route get 1.1.1.1
[2026-08-09T17:21:07.568002900+0800] 53265 CMD docker stop -t 5 trawl
[2026-08-09T17:21:07.585323579+0800] 53275 CMD docker events --filter container=binhex-qbittorrentvpn ...
[2026-08-09T17:21:07.614892635+0800] 53294 CMD docker inspect -f \{\{.State.Running\}\} trawl
  ... (repeats)
[2026-08-09T17:21:11.471897898+0800] 55250 CMD docker events --filter container=binhex-qbittorrentvpn ...
=== script exit code: 124 (124 = killed by timeout after 4s) ===
VERDICT: REJECTED — cannot complete a dry run
  - (a) did not terminate within the timeout (script was killed)
  - (b) unshimmed binary invoked: /usr/bin/mkdir
```
175 shimmed calls (35 full loop iterations, each calling `docker inspect`,
two `docker exec` probes, `docker stop`, and `docker events`) in 4 seconds
— versus roughly 0.13 iterations of the real cadence in the same 4 seconds.
A shim that answers instantly instead of blocking turns a well-behaved
30-second poller into a busy loop that also repeatedly calls `docker stop`
on a real container name, every time, because the fixture's fake network
state can never satisfy `safe()`. This is why branch (a) had to include
"did not terminate" as its own case: the honest way to fail a script whose
design assumption (a blocking, slow subcommand) the shim cannot honor is to
say so, not to let it spin until the timeout looks like an unrelated crash.

## Deliberate scope decision: `docker` is opaque, never split by subcommand

The shim treats every `docker` invocation identically — logged, canned,
never touching the real socket — rather than letting read-only subcommands
(`docker ps`, `docker inspect`) hit the real daemon while write subcommands
are intercepted. This was a conscious call, not an oversight: classifying
which of dozens of docker subcommands are safe to let through is real
scope, and issue #51 already treats the docker socket as root-equivalent
("container isolation is not a security boundary"). The direct consequence
is Question 3's divergence finding — **any** script that branches on docker
state necessarily receives fabricated data during a dry run. That's a
sharper, more honest answer than half-heartedly making some docker calls
"real" and leaving the classification gap implicit.

A second consequence, visible in `trawl-vpn-guard`'s transcript: `docker
exec "$VPN" sh -c "redis-cli ... del ..."` is logged as one opaque `docker
exec` call. Whatever happens inside that container — including further
mutating commands like `redis-cli DEL` — is invisible to a host-level PATH
shim, because it runs in a different mount and PID namespace entirely.
`docker exec` is a smuggling vector this architecture cannot see through.

## Real-environment fixture gaps (also evidence, not noise)

Two runs failed for reasons that are themselves informative rather than
implementation bugs:

- `viewDockerLogSize` (`du -ah /var/lib/docker/containers/ | ...`) — REJECTED
  under predicate (a), exit 1, because this dev machine's local Docker
  installation makes that directory root-owned and unreadable to the
  unprivileged user namespace. On the real Unraid host this script runs as
  root and reads fine. **The dry-run environment is not byte-identical to
  production in ways that matter** — uid/gid, installed packages, and
  mounted paths all differ, and a script that depends on any of them can be
  rejected (or worse, silently pass) for reasons that have nothing to do
  with what it would actually do on Unraid.
- `tdarr-plex-gate` and `trawl-vpn-guard` both reference real Unraid paths
  (`/mnt/user/appdata/Plex-Media-Server/...`) that don't exist on this
  machine at all; without a `DRYRUN_FIXTURE_MOUNTS` override they take their
  "resource unreachable" fail-safe branches (see `tdarr-plex-gate`'s
  Plex-unreachable path) rather than the branch a production run would
  usually take.

## Where the honest ceiling is

Plain language, safe for the dashboard to say to a non-expert:

> **A dry run shows what the script asked the sandbox to do, not what it
> would do on your server.** For simple scripts that don't depend on live
> container or file state, that's the same thing. For scripts that check
> "is this container running?" or "what does this command print?" before
> deciding what to do, the dry run has to guess the answer — and if its
> guess doesn't match your server's actual state at approval time, the real
> run can do something the dry run never showed you. The dry run also can't
> see inside anything it runs via `docker exec` — only that it ran. And a
> script written to run forever (a daemon-style script, not a one-shot
> maintenance task) cannot be dry-run to completion at all; the executor
> gives up after a timeout and that counts as a rejection, not a pass. **A
> clean dry run is evidence the script's authors intended for it to run
> safely under the conditions the sandbox guessed — never a guarantee of
> what will happen on your actual server.**

More precisely, for the engineering record:

1. **Divergence is structural, not incidental** (Question 3). Any shimmed
   command whose real stdout a script branches on is a place the transcript
   can be wrong. This prototype shims exactly `docker` for stdout-bearing
   branches; a production version would need to enumerate every such
   command a script might call, which is unbounded in general (arbitrary
   bash can call anything).
2. **The shim list is necessarily incomplete and needs continuous curation**
   (branch b's `mkdir` finding). A fixed list of "the twelve obviously
   mutating commands" missed a real, common one on the very first non-trivial
   real script tried. The read-only-safe allowlist is the same trade in
   reverse: an allowlist-of-safe fails closed (an unknown binary is
   rejected) where a blocklist-of-unsafe would fail open (an unknown binary
   runs for real) — this prototype chose fail-closed deliberately, at the
   cost of false positives needing curation over time (`realpath`, used by
   the shims' own boundary check, had to be added to the allowlist during
   this session).
3. **`docker exec` (and any other container-hopping primitive) is opaque.**
   A host-level PATH shim cannot see what happens inside a container.
4. **Long-running / daemon-shaped scripts cannot be dry-run to completion at
   all** — evidenced directly by `trawl-vpn-guard`'s runaway loop. The
   product needs a policy for this class (reject outright? require an
   `--once` mode? cap iterations?) that this prototype does not attempt to
   answer.
5. **The dry-run environment differs from production in ways a script can
   observe** (uid/gid, installed packages, mounted paths) — evidenced by
   `viewDockerLogSize`'s permission-denied and the two scripts' fail-safe
   branches when their real Unraid paths are absent. On the real product
   (running the executor on the Unraid host itself, per issue #51's "resident"
   architecture) most of this gap closes, but the fixture-mount mechanism
   this prototype needed to reach some code paths at all
   (`DRYRUN_FIXTURE_MOUNTS`) is evidence that *whatever* differs between the
   dry-run context and the live one is a place a script's dry-run behavior
   can be wrong.
6. **The write-boundary check is a textual heuristic**, not enforced by the
   kernel. It parses argv per command type (`extract_targets` in
   `bin/_shim_common.sh`) and is documented there as not handling every flag
   form (e.g. `cp -t DEST`). The kernel-level read-only mount is the second,
   independent layer of defense for anything the 12 shims don't cover
   (`mkdir`, `sed -i`, etc.) — but only catches attempts that actually reach
   a real syscall against a real path, which `docker`'s in-container
   mutations (point 3) never do.

## Answering the destination test directly

Can a shimmed executor produce a useful and honest transcript for a real
Unraid maintenance script? **Yes, for the class of script that doesn't
gate its behavior on live daemon/container state, and yes as evidence (never
proof) for the class that does — provided the UI states the ceiling above
plainly, the predicate rejects non-terminating scripts and unshimmed
mutation-risk binaries by default, and the product accepts that the shim
list and read-only-safe allowlist are living documents that need curation
as new real scripts are tried, not a one-time list.**
