# Per-Service config + config hot-reload

## What to build

Honor per-Service configuration — latency threshold, debounce N, poll interval
— from the YAML source of truth, and hot-reload the config file on change
without a restart and without losing runtime state. Config remains the single
source of truth; SQLite never holds config. An invalid config on reload is
rejected and the previous good config stays active.

Conforms to `CONTEXT.md` and `docs/CONVENTIONS.md` (rule 6).

## Acceptance criteria

- [ ] Per-Service latency threshold, debounce N, and poll interval are read and applied
- [ ] Editing the config file applies changes without a restart
- [ ] Invalid config on reload is rejected; previous good config stays active
- [ ] Runtime Status and history survive a hot-reload

## Blocked by

- 0002-end-to-end-http-monitoring-vertical
