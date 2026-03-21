package messages

import (
	"time"
)

// Memory represents a single persistent memory entry.
type Memory struct {
	ID           string     `json:"id"`
	Content      string     `json:"content"`
	Summary      string     `json:"summary"`
	OriginalID   string     `json:"original_id"`
	Tags         []string   `json:"tags"`
	Importance   float64    `json:"importance"`
	AccessCount  int        `json:"access_count"`
	LastAccessed time.Time  `json:"last_accessed"`
	CreatedAt    time.Time  `json:"created_at"`
	Source       string     `json:"source"`
	Embedding    []float64  `json:"embedding"`
	AlwaysLoad   bool       `json:"always_load"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Tokens       int        `json:"tokens"`
	RefType      string     `json:"ref_type"`
	RefTarget    string     `json:"ref_target"`
	IsLazy       bool       `json:"is_lazy"`
}

// BuildContextRequest specifies how to build context from memory
type BuildContextRequest struct {
	Tags              []string `json:"tags"`
	TokenBudget       int      `json:"token_budget"`
	MinImportance     float64  `json:"min_importance"`
	ExcludeTags       []string `json:"exclude_tags"`
	IncludeAlwaysLoad bool     `json:"include_always_load"`
	MaxChunkSize      int      `json:"max_chunk_size"`
	TruncateThreshold int      `json:"truncate_threshold"`
	TruncatePreview   int      `json:"truncate_preview"`
}

// PrepareSessionConfig configures what gets loaded into memory for a session
type PrepareSessionConfig struct {
	RootDir        string        `json:"root_dir"`
	IdentityFile   string        `json:"identity_file"`
	IdentityText   string        `json:"identity_text"`
	AgentName      string        `json:"agent_name"`
	RecencyWindow  time.Duration `json:"recency_window"`
	RecentFilesTTL time.Duration `json:"recent_files_ttl"`
}

// Session represents a tracked agent session.
type Session struct {
	ID           string    `json:"id"`
	AgentName    string    `json:"agent_name"`
	Model        string    `json:"model"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	Cost         float64   `json:"cost"`
	Summary      string    `json:"summary"`
	Tags         []string  `json:"tags"`
}

// ToolMetadata represents metadata about an available tool
type ToolMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

// CompactionResult describes what was compacted.
type CompactionResult struct {
	OriginalIDs []string `json:"original_ids"`
	NewID       string   `json:"new_id"`
	Tags        []string `json:"tags"`
	Count       int      `json:"count"`
}

// === Request/Response message types for each operation ===

// MemorySaveRequest - memory.save
type MemorySaveRequest struct {
	Memory Memory `json:"memory"`
}

type MemorySaveResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// MemoryGetRequest - memory.get
type MemoryGetRequest struct {
	ID string `json:"id"`
}

type MemoryGetResponse struct {
	Memory *Memory `json:"memory,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// MemorySearchRequest - memory.search
type MemorySearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type MemorySearchResponse struct {
	Memories []Memory `json:"memories"`
	Error    string   `json:"error,omitempty"`
}

// MemoryForgetRequest - memory.forget
type MemoryForgetRequest struct {
	ID string `json:"id"`
}

type MemoryForgetResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// MemoryDecayRequest - memory.decay
type MemoryDecayRequest struct {
}

type MemoryDecayResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// MemoryListRequest - memory.list
type MemoryListRequest struct {
	Limit         int     `json:"limit"`
	MinImportance float64 `json:"min_importance"`
}

type MemoryListResponse struct {
	Memories []Memory `json:"memories"`
	Error    string   `json:"error,omitempty"`
}

// MemoryCompactRequest - memory.compact
type MemoryCompactRequest struct {
	MinAge   time.Duration `json:"min_age"`
	MinCount int           `json:"min_count"`
}

type MemoryCompactResponse struct {
	Results []CompactionResult `json:"results"`
	Error   string             `json:"error,omitempty"`
}

// MemoryBuildContextRequest - memory.build-context
type MemoryBuildContextRequest struct {
	Request BuildContextRequest `json:"request"`
}

type MemoryBuildContextResponse struct {
	Memories   []Memory `json:"memories"`
	TokenCount int      `json:"token_count"`
	Error      string   `json:"error,omitempty"`
}

// MemoryPrepareSessionRequest - memory.prepare-session
type MemoryPrepareSessionRequest struct {
	Config PrepareSessionConfig `json:"config"`
}

type MemoryPrepareSessionResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// MemoryLoadToolRegistryRequest - memory.load-tool-registry
type MemoryLoadToolRegistryRequest struct {
	Tools []ToolMetadata `json:"tools"`
}

type MemoryLoadToolRegistryResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// MemoryUpdateToolUsageRequest - memory.update-tool-usage
type MemoryUpdateToolUsageRequest struct {
	ToolName   string `json:"tool_name"`
	Summary    string `json:"summary"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

type MemoryUpdateToolUsageResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// MemorySessionSaveRequest - memory.session-save
type MemorySessionSaveRequest struct {
	Session Session `json:"session"`
}

type MemorySessionSaveResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// MemoryTrackUsageRequest - memory.track-usage
type MemoryTrackUsageRequest struct {
	MemoryID   string `json:"memory_id"`
	SessionID  string `json:"session_id"`
	TurnNumber int    `json:"turn_number"`
	UsageType  string `json:"usage_type"`
}

type MemoryTrackUsageResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}