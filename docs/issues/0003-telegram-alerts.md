# Telegram Alerts on committed change + recovery

## What to build

One-way Alerts to the Operator over Telegram, behind the `Notifier` seam
(`go-telegram/bot`). An Alert fires on a committed Status change and on
recovery to UP. The debounce from the HTTP vertical absorbs flapping, so a
flapping Service yields one "down" and one "back up" message, never a storm.
The bot token is supplied via env/file, never the committed YAML, never logged.
Strictly one-way in v1 (two-way Approval is M2, behind the same seam).

Conforms to `CONTEXT.md` (Alert vs Approval) and `docs/CONVENTIONS.md` (rules 2, 7).

## Acceptance criteria

- [ ] `Notifier` seam implemented with `go-telegram/bot`; token from env/file, never logged
- [ ] Committed DOWN/DEGRADED sends exactly one Alert
- [ ] Recovery to UP sends exactly one Alert
- [ ] A flapping Service produces one down + one recovery message, not per-poll spam

## Blocked by

- 0002-end-to-end-http-monitoring-vertical

(External prerequisite for verification: a Telegram bot token + a chat to receive Alerts.)
