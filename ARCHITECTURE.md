# Bus Architecture

Bus is a **dumb pub/sub pipe with persistence**. It does not route, does not know about agents, does not make decisions.

## Role

- Pub/sub message delivery
- SQLite persistence (every message logged)
- WebSocket subscriptions with consumer tracking + ack
- Queueing (messages persist even if no consumer is connected)

## Connections

```
SI → Bus (HTTP publish, WS subscribe)
Adapters → Bus (HTTP publish, WS subscribe)  [future: adapters talk to bus directly, not through SI]
Inber → Bus (WS subscribe for inbound, HTTP publish for outbound + events)
```

## Topics

| Topic | Direction | Content |
|-------|-----------|---------|
| `inbound` | user/adapter → agent | Chat messages with agent routing metadata |
| `outbound` | agent → user/adapter | Agent responses |
| `events` | system → observers | Spawn started/done, deploy, workspace, health |

## What Changes

- **Nothing structural** — bus stays exactly as it is
- **Add**: `events` topic (bus doesn't care, it's just another topic string)
- **Remove**: bus-agent (`cmd/bus-agent/`) — routing logic moves to inber server

## What bus-agent Did (Moving to Inber)

bus-agent was a dispatcher process that:
1. Subscribed to bus `inbound` topic
2. Resolved which agent handles a message (routing table)
3. Called the backend (inber CLI, OpenClaw, etc.)
4. Published response to `outbound`
5. Managed per-agent queues, forge slots, model dashboard API

All of this moves to inber server, which subscribes to bus directly.

## Interfaces

Bus exposes HTTP + WebSocket. No Go interfaces needed — it's a standalone service.

```
POST /publish   {topic, payload, source}
POST /ack       {consumer, topic, message_id}
GET  /history   ?topic=X&limit=N
GET  /stats
WS   /subscribe ?consumer=X&topics=a,b,c
```
