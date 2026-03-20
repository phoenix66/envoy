# Envoy

![Go version](https://img.shields.io/badge/go-1.25+-blue)

A self-hosted SMTP journaling relay for independent mail archiving

## Overview

Envoy sits between your mail infrastructure and a journaling archive (such as
MailArchiva). It accepts messages via SMTP on both the standard inbound port
(25) and the client submission port (587), builds an Exchange-style journal
report envelope for each message, and delivers both the original message and
the journal envelope to their respective destinations — all through a
persistent store-and-forward queue that retries on failure.

```
Sending MTA ──► Envoy :25/:587 ──► Next-hop MTA  (original message)
                                └──► Archive SMTP  (journal envelope)
```

Key properties:

- **No message loss** — messages are written to a bbolt queue before the SMTP
  DATA response is issued; they survive process restarts.
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
  queue/      Persistent bbolt queue — forward + journal buckets, dead-letter
  delivery/   SMTPSender, Deliverer (enqueue), Worker (poll + send)
cmd/
  envoy/      Wire-up, signal handling, graceful shutdown
```

**Data flow**

1. A message arrives on `:25` (inbound) or `:587` (submission with AUTH).
2. The SMTP session validates recipients and reads the full message body.
3. The message handler builds a journal envelope and enqueues two entries:
   - `forward` bucket → relay to the domain's configured next-hop MTA.
   - `journal` bucket → deliver journal envelope to the archive server.
4. The SMTP session returns `250 OK` — the sending MTA is released.
5. Two background workers (one per bucket) poll for due entries and deliver
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
  smtp_host:    archive.yourdomain.com
  journal_from: journal@yourdomain.com
  journal_to:   archive@yourdomain.com

auth:
  users:
    - username: submituser
      password_hash: "$2a$10$..."   # bcrypt hash
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

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
