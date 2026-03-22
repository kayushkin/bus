package messages

// Standard NATS request/reply envelope for service APIs.
// All service queries use this pattern:
//   Request → NATS subject (e.g. "agents.list") → Response
//
// Subjects follow: <domain>.<action>
//   agents.list, agents.get
//   sessions.list
//   usage.query
//   logs.query (already exists via logstack)
//   memory.* (already exists via agent-store)

// APIRequest is the standard request envelope for NATS service queries.
type APIRequest struct {
	Action string         `json:"action,omitempty"` // optional — subject often implies action
	Params map[string]any `json:"params,omitempty"` // query parameters
}

// APIResponse is the standard response envelope for NATS service queries.
type APIResponse struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// --- Agents domain (agent-store) ---

// AgentEntry is a registered agent with its orchestrator association.
type AgentEntry struct {
	Name         string `json:"name"`
	Orchestrator string `json:"orchestrator"`
	Description  string `json:"description,omitempty"`
	Emoji        string `json:"emoji,omitempty"`
	Project      string `json:"project,omitempty"`
	Enabled      bool   `json:"enabled"`
	IsDefault    bool   `json:"is_default,omitempty"`
}

// --- Sessions domain (orchestrators) ---

// SessionEntry represents an active or recent session from any orchestrator.
type SessionEntry struct {
	ID            string `json:"id"`
	Agent         string `json:"agent"`
	Orchestrator  string `json:"orchestrator"`
	Status        string `json:"status"`                   // idle, running, completed, error
	SpawnDepth    int    `json:"spawn_depth,omitempty"`
	ParentKey     string `json:"parent_key,omitempty"`
	Messages      int    `json:"messages,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
	LastActive    string `json:"last_active,omitempty"`
	ActiveRequest string `json:"active_request,omitempty"` // current input text if running
}

// --- Usage domain (model-store) ---

// UsageQuery specifies filters for usage data.
type UsageQuery struct {
	Days         int    `json:"days,omitempty"`          // last N days (default 7)
	Agent        string `json:"agent,omitempty"`         // filter by agent
	Model        string `json:"model,omitempty"`         // filter by model
	Orchestrator string `json:"orchestrator,omitempty"`  // filter by orchestrator
}

// UsageEntry is a single usage record.
type UsageEntry struct {
	Date         string  `json:"date"`
	Agent        string  `json:"agent"`
	Orchestrator string  `json:"orchestrator,omitempty"`
	Model        string  `json:"model"`
	Provider     string  `json:"provider,omitempty"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Requests     int     `json:"requests"`
	CostUSD      float64 `json:"cost_usd"`
}

// UsageResponse wraps usage data by period.
type UsageResponse struct {
	Day   []UsageEntry `json:"day"`
	Week  []UsageEntry `json:"week"`
	Month []UsageEntry `json:"month"`
}
