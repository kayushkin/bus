package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// UsageStats holds aggregated token usage for an agent.
type UsageStats struct {
	Agent        string `json:"agent"`
	Orchestrator string `json:"orchestrator"`
	Model        string `json:"model,omitempty"`
	Messages     int    `json:"messages"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
	DurationMs   int64  `json:"duration_ms"`
}

// UsageResponse is the API response for /api/usage.
type UsageResponse struct {
	Day   []UsageStats `json:"day"`   // last 24 hours
	Week  []UsageStats `json:"week"`  // last 7 days
	Month []UsageStats `json:"month"` // last 30 days
}

func (ba *BusAgent) handleUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	logstackURL := "http://localhost:8088" // TODO: make configurable

	now := time.Now().UTC()
	day := now.Add(-24 * time.Hour)
	week := now.Add(-7 * 24 * time.Hour)
	month := now.Add(-30 * 24 * time.Hour)

	resp := UsageResponse{
		Day:   aggregateUsage(ba.http, logstackURL, day),
		Week:  aggregateUsage(ba.http, logstackURL, week),
		Month: aggregateUsage(ba.http, logstackURL, month),
	}

	json.NewEncoder(w).Encode(resp)
}

func aggregateUsage(client *http.Client, logstackURL string, from time.Time) []UsageStats {
	// Query logstack for outbound messages since `from`.
	url := fmt.Sprintf("%s/api/v1/logs?type=outbound&from=%s&limit=10000",
		logstackURL, from.Format(time.RFC3339))

	resp, err := client.Get(url)
	if err != nil {
		log.Printf("[usage] logstack query error: %v", err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Logs []struct {
			Agent   string `json:"agent"`
			Channel string `json:"channel"`
			Content struct {
				Agent        string `json:"agent"`
				Orchestrator string `json:"orchestrator"`
				Meta         *struct {
					InputTokens     int    `json:"input_tokens"`
					OutputTokens    int    `json:"output_tokens"`
					DurationMs      int64  `json:"duration_ms"`
					Model           string `json:"model"`
					CacheReadTokens int    `json:"cache_read_tokens"`
				} `json:"meta"`
			} `json:"content"`
		} `json:"logs"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		log.Printf("[usage] logstack parse error: %v", err)
		return nil
	}

	// Aggregate by agent+orchestrator.
	type key struct{ agent, orch string }
	agg := make(map[key]*UsageStats)

	for _, entry := range result.Logs {
		agent := entry.Content.Agent
		orch := entry.Content.Orchestrator
		if agent == "" {
			agent = entry.Agent
		}

		meta := entry.Content.Meta
		if meta == nil {
			continue
		}

		k := key{agent, orch}
		stats, ok := agg[k]
		if !ok {
			stats = &UsageStats{
				Agent:        agent,
				Orchestrator: orch,
				Model:        meta.Model,
			}
			agg[k] = stats
		}

		stats.Messages++
		stats.InputTokens += meta.InputTokens
		stats.OutputTokens += meta.OutputTokens
		stats.TotalTokens += meta.InputTokens + meta.OutputTokens
		stats.DurationMs += meta.DurationMs
	}

	out := make([]UsageStats, 0, len(agg))
	for _, s := range agg {
		out = append(out, *s)
	}
	return out
}
