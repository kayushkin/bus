package messages

// SpawnStarted is published when a sub-agent session begins.
type SpawnStarted struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Task      string `json:"task"`
}

// SpawnCompleted is published when a sub-agent session finishes.
type SpawnCompleted struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	Success   bool   `json:"success"`
}
