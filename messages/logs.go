package messages

import "time"

// LogEntry represents a single log entry in the standard format.
// This is a standalone copy of the logstack model — no dependency on logstack.
type LogEntry struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Source    string                 `json:"source"`
	Agent     string                 `json:"agent,omitempty"`
	Channel   string                 `json:"channel,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	Model     string                 `json:"model,omitempty"`
	Level     string                 `json:"level"`
	Type      string                 `json:"type"`
	Content   interface{}            `json:"content"`
	TokensIn  int                    `json:"tokens_in,omitempty"`
	TokensOut int                    `json:"tokens_out,omitempty"`
	LatencyMs int64                  `json:"latency_ms,omitempty"`
	Error     string                 `json:"error,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// LogType constants.
const (
	TypeMessage    = "message"
	TypeToolCall   = "tool_call"
	TypeToolResult = "tool_result"
	TypeError      = "error"
	TypeMetrics    = "metrics"
	TypeLifecycle  = "lifecycle"
	TypeRouting    = "routing"
)

// Level constants.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)
