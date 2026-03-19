package messages

import "time"

// DownloadRequest represents a request to download media content.
type DownloadRequest struct {
	ID        string    `json:"id"`         // unique request ID
	URL       string    `json:"url"`        // primary download URL
	Fallbacks []string  `json:"fallbacks,omitempty"` // fallback URLs if primary fails
	Type      string    `json:"type,omitempty"`      // "book", "manga", "podcast", "video"
	Timestamp time.Time `json:"timestamp"`
}

// DownloadStatus represents the status of a download job.
type DownloadStatus struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	Status     string    `json:"status"` // queued, running, done, error, retry
	Error      string    `json:"error,omitempty"`
	Filename   string    `json:"filename,omitempty"`
	RetryCount int       `json:"retry_count,omitempty"`
	NextRetry  string    `json:"next_retry,omitempty"`
	StartedAt  string    `json:"started_at,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}
