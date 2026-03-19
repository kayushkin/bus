# DEPRECATED

bus-agent is replaced by inber server's built-in bus listener (server/bus.go).

Inber server now:
- Subscribes to bus `inbound` topic directly
- Routes messages to agents via its own route table
- Publishes responses to `outbound` and events to `events`

This code is kept for reference only. Do not run bus-agent alongside inber server
— they would both consume from the same `inbound` topic and fight over messages.

Replaced: 2026-03-15
