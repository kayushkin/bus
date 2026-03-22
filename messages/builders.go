package messages

import (
	"fmt"
	"strings"
	"time"
)

// NewChatDelta creates a ChatDelta with all required fields.
// Empty strings panic — every field is required.
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

// NewChatOutbound creates a completed response message.
func NewChatOutbound(agent, orchestrator, sessionID, text string) ChatOutbound {
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
	if text == "" {
		missing = append(missing, "text")
	}
	if len(missing) > 0 {
		panic(fmt.Sprintf("messages.NewChatOutbound: required fields empty: %s", strings.Join(missing, ", ")))
	}
	return ChatOutbound{
		Agent:        agent,
		Orchestrator: orchestrator,
		SessionID:    sessionID,
		Text:         text,
		Timestamp:    time.Now(),
	}
}

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
	if d.Type == "" {
		missing = append(missing, "type")
	}
	if len(missing) > 0 {
		return fmt.Errorf("ChatDelta missing required fields: %s", strings.Join(missing, ", "))
	}
	return nil
}
