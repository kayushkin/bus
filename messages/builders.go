package messages

import (
	"fmt"
	"strings"
	"time"
)

// Intentionally is a sentinel value for required string fields that are
// deliberately left empty. Use this instead of "" to signal intent:
//
//	NewChatDelta(messages.Intentionally, "openclaw", sessionID, streamID, "done")
const Intentionally = "\x00intentionally_empty"

// isSet returns true if the value is non-empty and not the Intentionally sentinel.
func isSet(v string) bool {
	return v != "" && v != Intentionally
}

// resolve returns "" if v is the Intentionally sentinel, otherwise v as-is.
func resolve(v string) string {
	if v == Intentionally {
		return ""
	}
	return v
}

// --- ChatDelta constructors ---

// NewChatDelta creates a ChatDelta with all required fields.
// Pass Intentionally instead of "" to explicitly leave a field empty.
// Empty strings for required fields will cause a panic.
func NewChatDelta(agent, orchestrator, sessionID, turnID, deltaType string) ChatDelta {
	var missing []string
	if agent == "" {
		missing = append(missing, "agent")
	}
	if orchestrator == "" {
		missing = append(missing, "orchestrator")
	}
	if sessionID == "" {
		missing = append(missing, "sessionID")
	}
	if turnID == "" {
		missing = append(missing, "turnID")
	}
	if deltaType == "" {
		missing = append(missing, "type")
	}
	if len(missing) > 0 {
		panic(fmt.Sprintf("messages.NewChatDelta: required fields empty: %s (use messages.Intentionally for deliberate empty)", strings.Join(missing, ", ")))
	}
	resolved := resolve(turnID)
	return ChatDelta{
		Agent:        resolve(agent),
		Orchestrator: resolve(orchestrator),
		SessionID:    resolve(sessionID),
		TurnID:       resolved,
		StreamID:     resolved, // backward compat
		Type:         resolve(deltaType),
	}
}

// NewDoneDelta creates a done ChatDelta with optional meta.
func NewDoneDelta(agent, orchestrator, sessionID, turnID string, meta map[string]any) ChatDelta {
	d := NewChatDelta(agent, orchestrator, sessionID, turnID, "done")
	d.Done = true
	d.Meta = meta
	return d
}

// --- ChatOutbound constructors ---

// NewChatOutbound creates a ChatOutbound with all required fields.
// Pass Intentionally instead of "" to explicitly leave a field empty.
// Empty strings for required fields will cause a panic.
func NewChatOutbound(agent, orchestrator, channel, stream string) ChatOutbound {
	var missing []string
	if agent == "" {
		missing = append(missing, "agent")
	}
	if orchestrator == "" {
		missing = append(missing, "orchestrator")
	}
	if channel == "" {
		missing = append(missing, "channel")
	}
	if stream == "" {
		missing = append(missing, "stream")
	}
	if len(missing) > 0 {
		panic(fmt.Sprintf("messages.NewChatOutbound: required fields empty: %s (use messages.Intentionally for deliberate empty)", strings.Join(missing, ", ")))
	}
	return ChatOutbound{
		Agent:        resolve(agent),
		Orchestrator: resolve(orchestrator),
		Channel:      resolve(channel),
		Stream:       resolve(stream),
		Timestamp:    time.Now(),
	}
}

// --- Validation ---

// ValidateChatDelta checks that required fields are populated.
func ValidateChatDelta(d ChatDelta) error {
	var missing []string
	if d.Agent == "" {
		missing = append(missing, "agent")
	}
	if d.Orchestrator == "" {
		missing = append(missing, "orchestrator")
	}
	if d.SessionID == "" {
		missing = append(missing, "session_id")
	}
	if d.StreamID == "" {
		missing = append(missing, "stream_id")
	}
	if d.Type == "" {
		missing = append(missing, "type")
	}
	if len(missing) > 0 {
		return fmt.Errorf("ChatDelta missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// ValidateChatOutbound checks that required fields are populated.
func ValidateChatOutbound(d ChatOutbound) error {
	var missing []string
	if d.Agent == "" {
		missing = append(missing, "agent")
	}
	if d.Orchestrator == "" {
		missing = append(missing, "orchestrator")
	}
	if d.Channel == "" {
		missing = append(missing, "channel")
	}
	if d.Stream == "" {
		missing = append(missing, "stream")
	}
	if len(missing) > 0 {
		return fmt.Errorf("ChatOutbound missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}
