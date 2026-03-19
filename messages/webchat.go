package messages

// ChatDelta represents a streaming response delta on chat.stream.
type ChatDelta struct {
	Text      string `json:"text"`
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Done      bool   `json:"done,omitempty"`
	Type      string `json:"type"`              // "text", "thinking", "tool", "done", "no_reply", "system", "heartbeat"
	Category  string `json:"category,omitempty"` // "spawn_result", "subagent_summary", etc.
}

// WebchatSend is deprecated — use ChatInbound instead.
type WebchatSend = ChatInbound

// WebchatDelta is deprecated — use ChatDelta instead.
type WebchatDelta = ChatDelta
