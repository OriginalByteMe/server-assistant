# Deploy

Server Assistant deploys as one static binary plus one systemd unit (ADR 0004).
No container or language runtime is required on the target box.

## Install

Build on the release machine:

```sh
make build
```

Install on the target box:

```sh
sudo install -o root -g root -m 0755 bin/server-assistant /usr/local/bin/server-assistant
sudo useradd --system --no-create-home --shell /usr/sbin/nologin server-assistant
sudo install -d -o root -g root -m 0755 /etc/server-assistant
sudo install -d -o server-assistant -g server-assistant -m 0755 /var/lib/server-assistant
sudo install -o root -g root -m 0644 config.example.yaml /etc/server-assistant/config.yaml
sudo install -o root -g root -m 0600 /dev/null /etc/server-assistant/server-assistant.env
sudo install -o root -g root -m 0644 deploy/server-assistant.service /etc/systemd/system/server-assistant.service
```

Before starting the service, edit `/etc/server-assistant/config.yaml`. At
minimum, point SQLite state at the systemd state directory:

```yaml
database:
  path: /var/lib/server-assistant/server-assistant.db
```

## Config and secrets

`/etc/server-assistant/config.yaml` is the source of truth for monitored Hosts
and Services. SQLite under `/var/lib/server-assistant` is runtime state and
history only.

Put secrets in `/etc/server-assistant/server-assistant.env`, owned by root and
mode `0600`:

```sh
sudo chown root:root /etc/server-assistant/server-assistant.env
sudo chmod 600 /etc/server-assistant/server-assistant.env
```

Example env file:

```sh
TELEGRAM_BOT_TOKEN=...
TELEGRAM_CHAT_ID=...
UNRAID_ADDR=192.168.1.10:22
```

Reference secrets from YAML with `${VAR}` expansion, never by committing them to
YAML:

```yaml
host:
  name: unraid
  address: "${UNRAID_ADDR}"
telegram:
  bot_token: "${TELEGRAM_BOT_TOKEN}"
  chat_id: "${TELEGRAM_CHAT_ID}"
```

Start the unit after config and secrets are in place:

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now server-assistant
```

The daemon logs structured JSON to stdout; systemd sends stdout/stderr to
journald. No logfile path is configured.

## Logs

```sh
journalctl -u server-assistant -f
```

## Upgrade

Replace the binary, then restart the service:

```sh
sudo install -o root -g root -m 0755 bin/server-assistant /usr/local/bin/server-assistant
sudo systemctl restart server-assistant
```

## Verify

Confirm boot enablement:

```sh
systemctl is-enabled server-assistant
```

Confirm failure restart policy:

```sh
systemctl show server-assistant -p Restart -p RestartUSec
```

Expected values are `Restart=on-failure` and a 5 second restart delay.
