package messages

// AgentRunRequest is sent by the scheduler to request an agent turn.
type AgentRunRequest struct {
	Agent         string `json:"agent"`
	Prompt        string `json:"prompt"`
	Model         string `json:"model,omitempty"`
	SessionTarget string `json:"session_target"` // "main" or "isolated"
	JobID         string `json:"job_id,omitempty"`
}

// AgentRunResult is returned after an agent turn completes.
type AgentRunResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Agent   string `json:"agent"`
	Error   string `json:"error,omitempty"`
}
