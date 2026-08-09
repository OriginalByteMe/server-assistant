# Provenance

All six scripts under `real-scripts/` are byte-exact copies retrieved
read-only from the user's live Unraid box `rijkaardserver` (root over
Tailscale SSH, `100.90.134.29`), 2026-08-09. Nothing was executed on that
host — only `cat` and `stat`.

Retrieval:
```
ssh rijkaardserver "cat '/boot/config/plugins/user.scripts/scripts/<name>/script'"
```

Byte counts verified against `stat -c '%n %s'` on the host, matching the
local copies exactly (see session log; all seven `.../script` files matched
their remote size, one — `Bittorrent stalled torrent remover` — kept for a
reference but not used in FINDINGS.md, the other six all used):

| local dir | remote name | bytes |
|---|---|---|
| `bittorrent-stalled-torrent-remover/` | `Bittorrent stalled torrent remover` | 37 |
| `clamav_weekly_scan/` | `clamav_weekly_scan` | 576 |
| `delete_dangling_images/` | `delete_dangling_images` | 159 |
| `delete.ds_store/` | `delete.ds_store` | 175 |
| `tdarr-plex-gate/` | `tdarr-plex-gate` | 4075 |
| `trawl-vpn-guard/` | `trawl-vpn-guard` | 4534 |
| `viewDockerLogSize/` | `viewDockerLogSize` | 84 |

`description` files (where present) were copied alongside for context, also
unedited.

No synthetic/toy scripts were needed — all three "settle" questions in
issue #54 and all three rejection-predicate branches are demonstrated using
these real production scripts, run under different canned-response
scenarios (`scenarios/*.env`) or fixture-mount configurations
(`DRYRUN_FIXTURE_MOUNTS`, see README.md), never by editing the scripts
themselves.
