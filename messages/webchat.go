package messages

// ChatDelta represents a streaming response delta on chat.stream.
type ChatDelta struct {
	Agent        string     `json:"agent"`
	Orchestrator string     `json:"orchestrator"`
	SessionID    string     `json:"session_id"`
	CompletionID string     `json:"completion_id,omitempty"` // model provider completion ID, groups all events in a turn
	Type         string     `json:"type"`                    // text, thinking, tool, tool_result, done
	Text         string     `json:"text,omitempty"`
	Tool         string     `json:"tool,omitempty"`          // tool name for type=tool/tool_result
	ToolInput    string     `json:"tool_input,omitempty"`    // tool input summary
	ToolOutput   string     `json:"tool_output,omitempty"`   // tool output summary
	Stats        *TurnStats `json:"stats,omitempty"`         // only on type=done
}

// TurnStats contains token usage and cost info for a completed turn.
type TurnStats struct {
	InputTokens      int         `json:"input_tokens"`
	OutputTokens     int         `json:"output_tokens"`
	CacheReadTokens  int         `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int         `json:"cache_write_tokens,omitempty"`
	Cost             float64     `json:"cost,omitempty"`
	DurationMs       int         `json:"duration_ms,omitempty"`
	Model            string      `json:"model,omitempty"`
	Turn             int         `json:"turn,omitempty"`
	ToolCalls        int         `json:"tool_calls,omitempty"`
	Tools            []ToolEvent `json:"tools,omitempty"`
}

// ToolEvent describes a single tool invocation within a turn.
type ToolEvent struct {
	Tool   string `json:"tool"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// WebchatSend is deprecated — use ChatInbound instead.
type WebchatSend = ChatInbound

// WebchatDelta is deprecated — use ChatDelta instead.
type WebchatDelta = ChatDelta
