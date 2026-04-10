package messages

import "time"

// === chat.inbound ===

// ChatInbound represents an incoming chat message arriving via bus.
type ChatInbound struct {
	Text         string    `json:"text"`
	Author       string    `json:"author,omitempty"`
	Agent        string    `json:"agent,omitempty"`
	Orchestrator string    `json:"orchestrator,omitempty"` // "inber", "openclaw", etc.
	Channel      string    `json:"channel,omitempty"`      // "webchat", "discord", etc.
	SessionID    string    `json:"session_id,omitempty"`   // logical session for conversation continuity
	Timestamp    time.Time `json:"timestamp"`
}

// === chat.stream ===

// ChatDelta represents a streaming event on chat.stream.
// Types: text, thinking, tool, tool_result, done, spawn_started, spawn_completed
type ChatDelta struct {
	Agent        string     `json:"agent"`
	Orchestrator string     `json:"orchestrator"`
	SessionID    string     `json:"session_id"`
	CompletionID string     `json:"completion_id,omitempty"`
	Type         string     `json:"type"`
	Text         string     `json:"text,omitempty"`
	Tool         string     `json:"tool,omitempty"`
	ToolInput    string     `json:"tool_input,omitempty"`
	ToolOutput   string     `json:"tool_output,omitempty"`
	Stats        *TurnStats `json:"stats,omitempty"` // on type=done
	Hidden       bool       `json:"hidden,omitempty"` // entry is logged but should not display in frontends
}

// TurnStats contains token usage and cost info for a completed turn.
type TurnStats struct {
	InputTokens      int         `json:"input_tokens"`
	OutputTokens     int         `json:"output_tokens"`
	CacheReadTokens  int         `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int         `json:"cache_write_tokens,omitempty"`
	ContextTokens    int         `json:"context_tokens,omitempty"`  // actual context from last API call in the turn
	ContextLimit     int         `json:"context_limit,omitempty"`   // context window size reported by the model
	Cost             float64     `json:"cost,omitempty"`
	DurationMs       int         `json:"duration_ms,omitempty"`
	DurationAPIMs    int         `json:"duration_api_ms,omitempty"` // time spent in API calls only
	NumTurns         int         `json:"num_turns,omitempty"`       // total agentic turns in this run
	Model            string      `json:"model,omitempty"`
	Turn             int         `json:"turn,omitempty"`
	ToolCalls        int         `json:"tool_calls,omitempty"`
	Tools            []ToolEvent `json:"tools,omitempty"`
	APICalls         int            `json:"api_calls,omitempty"`       // number of API round-trips
	APICallUsages    []APICallUsage `json:"api_call_usages,omitempty"` // per-call token breakdown
}

// APICallUsage records token usage for a single API round-trip within a turn.
type APICallUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// ToolEvent describes a single tool invocation within a turn.
type ToolEvent struct {
	Tool   string `json:"tool"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// === chat.outbound ===

// ChatOutbound represents a completed agent response on chat.outbound (JetStream).
// This is the source of truth for what was said — used for history and persistence.
type ChatOutbound struct {
	Agent        string     `json:"agent"`
	Orchestrator string     `json:"orchestrator"`
	SessionID    string     `json:"session_id"`
	CompletionID string     `json:"completion_id,omitempty"`
	Text         string     `json:"text"`
	Stats        *TurnStats `json:"stats,omitempty"`
	Timestamp    time.Time  `json:"timestamp"`
}
