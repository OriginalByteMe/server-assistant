# systemd unit + deploy doc

## What to build

A systemd unit (auto-restart, boot-start, journald-friendly stdout logging) and
a concise deploy doc for running the binary on the separate box per ADR 0004
and `docs/CONVENTIONS.md`. No containerization — the value is one binary + one unit file. The deploy doc
covers install, config/secret placement, and the upgrade path (replace binary,
restart service).

## Acceptance criteria

- [ ] systemd unit runs the binary, restarts on failure, starts on boot
- [ ] Logs land in journald via stdout `slog`
- [ ] Deploy doc covers install, config/secret placement, and upgrade (replace binary + restart)

## Blocked by

- 0002-end-to-end-http-monitoring-vertical
