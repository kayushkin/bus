package messages

import (
	"fmt"
	"strings"
	"time"
)

// --- ChatDelta constructors ---

// NewChatDelta creates a ChatDelta with all required fields.
// Empty strings panic — every field is required, no exceptions.
func NewChatDelta(agent, orchestrator, sessionID, deltaType string) ChatDelta {
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
	if deltaType == "" {
		missing = append(missing, "type")
	}
	if len(missing) > 0 {
		panic(fmt.Sprintf("messages.NewChatDelta: required fields empty: %s", strings.Join(missing, ", ")))
	}
	return ChatDelta{
		Agent:        agent,
		Orchestrator: orchestrator,
		SessionID:    sessionID,
		Type:         deltaType,
	}
}

// NewDoneDelta creates a done ChatDelta with optional stats.
func NewDoneDelta(agent, orchestrator, sessionID string, stats *TurnStats) ChatDelta {
	d := NewChatDelta(agent, orchestrator, sessionID, "done")
	d.Stats = stats
	return d
}

// --- ChatOutbound constructors ---

// NewChatOutbound creates a ChatOutbound with all required fields.
// Empty strings panic — every field is required, no exceptions.
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
		panic(fmt.Sprintf("messages.NewChatOutbound: required fields empty: %s", strings.Join(missing, ", ")))
	}
	return ChatOutbound{
		Agent:        agent,
		Orchestrator: orchestrator,
		Channel:      channel,
		Stream:       stream,
		Timestamp:    time.Now(),
	}
}

// --- Validation ---

// ValidateChatDelta checks that required fields are populated.
// Returns error describing which fields are missing.
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
