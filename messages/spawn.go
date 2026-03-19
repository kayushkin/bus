package messages

// SpawnStarted is published when a sub-agent session begins.
type SpawnStarted struct {
	Agent         string `json:"agent"`
	SessionID     string `json:"session_id"`
	Task          string `json:"task"`
	Orchestrator  string `json:"orchestrator,omitempty"`
	ParentSession string `json:"parent_session,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
}

// SpawnCompleted is published when a sub-agent session finishes.
type SpawnCompleted struct {
	Agent         string `json:"agent"`
	SessionID     string `json:"session_id"`
	Result        string `json:"result"`
	Success       bool   `json:"success"`
	Orchestrator  string `json:"orchestrator,omitempty"`
	ParentSession string `json:"parent_session,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
	Timestamp     string `json:"timestamp,omitempty"`
}
