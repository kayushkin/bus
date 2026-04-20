package messages

import (
	"time"

	"github.com/kayushkin/llm-bridge/msg"
)

// Subjects for the llm-bridge adapter. All participants — llm-bridge-adapter
// and any bus producer/consumer — must use these constants to publish or
// subscribe.
const (
	// BridgeInboundSubject is core NATS: a request for the adapter to create
	// a session, update its config, and/or send a message to it.
	BridgeInboundSubject = "bridge.inbound"

	// BridgeEventSubject is core NATS: every SSE event emitted by llm-bridge
	// for a bus-owned session, wrapped as a BridgeEvent. Live-only (no
	// replay). Consumers that need durability should also subscribe to
	// BridgeResultSubject.
	BridgeEventSubject = "bridge.event"

	// BridgeResultSubject is JetStream: completed turns (Event.Type ==
	// msg.EventResult) wrapped as BridgeEvent. Durable — new subscribers see
	// history.
	BridgeResultSubject = "bridge.result"
)

// BridgeInbound wraps a canonical llm-bridge request for delivery over NATS.
//
// The wrapper only carries bus-layer routing; the inner Create/Send/Config
// fields are the exact llm-bridge HTTP request bodies and are passed to the
// bridge server unchanged. Producers set the llm-bridge fields they would
// have POSTed over HTTP; the adapter unwraps and forwards.
//
// A given message may contain any non-empty subset of Create, Config, Send —
// the adapter applies them in that order. On the first turn a producer
// typically sets Create+Send (and optionally Config). Subsequent turns
// normally send just Send; the adapter reuses the bridge session it created
// earlier for the same BusSessionID.
type BridgeInbound struct {
	BusSessionID string    `json:"bus_session_id"`
	Agent        string    `json:"agent,omitempty"`
	Orchestrator string    `json:"orchestrator,omitempty"`
	Channel      string    `json:"channel,omitempty"`
	Timestamp    time.Time `json:"timestamp"`

	Create *msg.CreateSessionRequest `json:"create,omitempty"`
	Config *msg.ConfigSessionRequest `json:"config,omitempty"`
	Send   *msg.SendMessageRequest   `json:"send,omitempty"`
}

// BridgeEvent wraps a canonical llm-bridge Event for delivery over NATS.
//
// Event is the full unmodified SSE payload from the bridge server. The
// wrapper adds only the bus-layer routing fields consumers need to
// correlate events with the originating BridgeInbound.
type BridgeEvent struct {
	BusSessionID string    `json:"bus_session_id"`
	Agent        string    `json:"agent,omitempty"`
	Orchestrator string    `json:"orchestrator,omitempty"`
	Timestamp    time.Time `json:"timestamp"`

	Event msg.Event `json:"event"`
}
