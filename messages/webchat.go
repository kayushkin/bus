package messages

// ChatDelta represents a streaming response delta on chat.stream.
type ChatDelta struct {
	Text          string         `json:"text"`
	Agent         string         `json:"agent"`
	Orchestrator  string         `json:"orchestrator,omitempty"`
	SessionID     string         `json:"session_id"`
	TurnID        string         `json:"turn_id,omitempty"`        // unique within session (inber: turn number, openclaw: message id)
	ParentSession string         `json:"parent_session,omitempty"` // parent session key if sub-agent
	StreamID      string         `json:"stream_id,omitempty"`      // deprecated: use TurnID instead
	Done          bool           `json:"done,omitempty"`
	Type          string         `json:"type"`                     // "text", "thinking", "tool", "tool_result", "done", "no_reply", "system", "heartbeat"
	Tool          string         `json:"tool,omitempty"`           // tool name for type="tool"/"tool_result"
	ToolInput     string         `json:"tool_input,omitempty"`     // tool input summary
	ToolOutput    string         `json:"tool_output,omitempty"`    // tool output summary
	Category      string         `json:"category,omitempty"`       // "spawn_result", "subagent_summary", etc.
	Meta          map[string]any `json:"meta,omitempty"`           // token usage, cost, model info on done events
}

// WebchatSend is deprecated — use ChatInbound instead.
type WebchatSend = ChatInbound

// WebchatDelta is deprecated — use ChatDelta instead.
type WebchatDelta = ChatDelta
