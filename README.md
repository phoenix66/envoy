# Envoy

![Go version](https://img.shields.io/badge/go-1.25+-blue)

A self-hosted SMTP journaling relay for independent mail archiving

## Overview

Envoy sits between your mail infrastructure and one or more journaling archives
and forwarding destinations. It accepts messages via SMTP on both the standard
inbound port (25) and the client submission port (587), builds an Exchange-style
journal report envelope for each message, and delivers the original message and
journal envelopes to all configured destinations — all through a persistent
store-and-forward queue that retries on failure.

Each destination has its own independent queue bucket, worker goroutine, and
retry state, so a slow or unavailable target never blocks the others.

```
Sending MTA ──► Envoy :25/:587 ──► forward targets  (original message, verbatim)
                                └──► archive targets  (journal envelope per target)
```

Key properties:

- **No message loss** — messages are written to a bbolt queue before the SMTP
  DATA response is issued; they survive process restarts.
- **Multiple destinations** — any number of archive and forward targets, each
  with an independent queue and retry state.
- **Retry with backoff** — configurable retry intervals; exhausted entries move
  to a dead-letter bucket for operator inspection.
- **Typed SMTP errors** — 4xx responses trigger retries; 5xx responses
  dead-letter immediately without pointless retry cycles.
- **Dual-port with AUTH** — port 25 accepts unauthenticated inbound relay;
  port 587 requires PLAIN or LOGIN SASL credentials (bcrypt-hashed in config).
- **STARTTLS** — advertised on both ports; opportunistic for outbound delivery.

## Architecture

```
internal/
  config/     Load and validate YAML config (viper + mapstructure)
  message/    Shared InboundMessage type; NewID() for unique message IDs
  smtp/       Inbound SMTP server — two listeners, go-smtp backend + session
  journal/    Build Exchange-style multipart/report journal envelopes
  queue/      Persistent bbolt queue — per-destination buckets, dead-letter
  delivery/   SMTPSender, Deliverer (enqueue), Worker (poll + send)
cmd/
  envoy/      Wire-up, signal handling, graceful shutdown
```

**Data flow**

1. A message arrives on `:25` (inbound) or `:587` (submission with AUTH).
2. The SMTP session validates recipients and reads the full message body.
3. The message handler enqueues one entry per configured destination:
   - `forward.<name>` bucket → relay the original message to a forward target.
   - `archive.<name>` bucket → deliver a journal envelope to an archive target.
4. The SMTP session returns `250 OK` — the sending MTA is released.
5. One background worker per destination polls for due entries and delivers
   them, retrying on temporary failures and dead-lettering on permanent ones.

## Installation

**Requirements**

Go **1.25.0 or newer** is required. `go.mod` was auto-upgraded from 1.22 to
1.25.0 when `golang.org/x/crypto` was resolved at dependency time. A toolchain
older than 1.25 will refuse to build with a version mismatch error rather than
a helpful diagnostic.

**From source**

```bash
git clone https://github.com/phoenix66/envoy.git
cd envoy
make build              # produces ./envoy
sudo cp envoy /usr/local/bin/
```

**Docker**

```bash
docker pull ghcr.io/phoenix66/envoy:latest
# or build locally:
make docker-build
```

**docker-compose**

```bash
cp configs/config.example.yaml configs/config.yaml
$EDITOR configs/config.yaml      # fill in required fields
docker compose up -d
```

## Configuration

Copy `configs/config.example.yaml` and edit it. Every field is documented
inline in that file.

**Minimum required fields**

```yaml
server:
  hostname: mail.yourdomain.com
  tls:
    enabled: true
    cert: /etc/envoy/tls.crt
    key:  /etc/envoy/tls.key

domains:
  - name: yourdomain.com
    next_hop: mx.yourdomain.com:25

archive:
  targets:
    - name: mailarchiva
      smtp_host:    archive.yourdomain.com
      journal_from: journal@yourdomain.com
      journal_to:   archive@yourdomain.com

auth:
  users:
    - username: submituser
      password_hash: "$2a$10$..."   # bcrypt hash
```

Multiple archive and forward targets are supported — each entry in the list
gets its own independent queue bucket and worker:

```yaml
archive:
  targets:
    - name: primary-archive
      smtp_host: archive1.yourdomain.com
      journal_from: journal@yourdomain.com
      journal_to:   archive@yourdomain.com
    - name: backup-archive
      smtp_host: archive2.yourdomain.com
      journal_from: journal@yourdomain.com
      journal_to:   backup@yourdomain.com

forward:
  targets:
    - name: espocrm
      smtp_host: crm.yourdomain.com
      smtp_port: 25
      from: relay@yourdomain.com
```

**Generate a bcrypt password hash**

```bash
# Python (available everywhere)
python3 -c "import bcrypt; print(bcrypt.hashpw(b'yourpassword', bcrypt.gensalt()).decode())"

# htpasswd (Apache utils)
htpasswd -bnBC 10 "" yourpassword | tr -d ':\n'
```

**Runtime flags**

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `/etc/envoy/config.yaml` | Path to config file |
| `--dev` | `false` | Console logger instead of JSON |

## Upgrading from v0.1

v0.2.0 contains breaking config and queue changes:

- `archive:` is now `archive.targets:` — a list of named target blocks.
- `forward:` is a new top-level section with a `targets:` list.
- Queue bucket names changed to `archive.<name>` / `forward.<name>`.
  Existing queue databases from v0.1 are not compatible; drain the queue
  before upgrading.

See [CHANGELOG.md](CHANGELOG.md) for the full list of changes.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
