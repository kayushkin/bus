# Bus

Lightweight message bus for the inber ecosystem. Go + SQLite.

Replaces the si tunnel architecture with a central pub/sub bus that all services connect to as peers.

## Architecture

```
Dashboard ──┐
Inber (WSL) ─┤── Bus ──┤── Discord (via si)
Android ────┘         └── Logstack
```

## API

### HTTP

- `POST /publish` — publish a message to a topic
- `POST /ack` — acknowledge message delivery
- `GET /history` — query message history

### WebSocket

- `WS /subscribe` — real-time subscription with catch-up

## Storage

SQLite with WAL mode. Messages persisted until acknowledged by all consumers.

## Build

```bash
go build -o bus ./cmd/bus/
```

## Run

```bash
./bus --addr :8100 --db bus.db
```
