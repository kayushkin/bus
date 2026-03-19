package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// --- /api/services ---

type serviceCheck struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
}

func (ba *BusAgent) handleServices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	type target struct {
		Name string
		Host string
		Port int
		URL  string
	}

	targets := []target{
		{"bus", "wsl", 8100, "http://localhost:8100/health"},
		{"bus-agent", "wsl", 8101, "http://localhost:8101/api/health"},
		{"inber", "wsl", 8200, "http://localhost:8200/health"},
		{"openclaw", "wsl", 18789, "http://localhost:18789/health"},
		{"logstack", "server", 8088, "http://localhost:8088/health"},
		{"forge", "server", 8150, "http://localhost:8150/health"},
	}

	client := &http.Client{Timeout: 2 * time.Second}
	results := make([]serviceCheck, len(targets))

	type indexedResult struct {
		idx int
		sc  serviceCheck
	}
	ch := make(chan indexedResult, len(targets))

	for i, t := range targets {
		go func(idx int, t target) {
			sc := serviceCheck{
				Name: t.Name,
				Host: t.Host,
				Port: t.Port,
			}
			start := time.Now()
			resp, err := client.Get(t.URL)
			sc.LatencyMs = time.Since(start).Milliseconds()
			if err != nil {
				sc.Status = "down"
			} else {
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				if resp.StatusCode >= 200 && resp.StatusCode < 400 {
					sc.Status = "up"
					if len(body) > 0 && len(body) < 4096 {
						sc.Detail = string(body)
					}
				} else {
					sc.Status = "down"
				}
			}
			ch <- indexedResult{idx, sc}
		}(i, t)
	}

	for range targets {
		res := <-ch
		results[res.idx] = res.sc
	}

	json.NewEncoder(w).Encode(results)
}

// --- /api/usage ---

func (ba *BusAgent) handleUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	dbPath := expandHome("~/.config/model-store/store.db")
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		http.Error(w, `{"error":"failed to open usage db"}`, http.StatusInternalServerError)
		return
	}
	defer db.Close()

	type usageStats struct {
		Agent        string `json:"agent"`
		Orchestrator string `json:"orchestrator"`
		Model        string `json:"model"`
		Messages     int    `json:"messages"`
		InputTokens  int64  `json:"input_tokens"`
		OutputTokens int64  `json:"output_tokens"`
		TotalTokens  int64  `json:"total_tokens"`
		DurationMs   int64  `json:"duration_ms"`
	}

	queryPeriod := func(days int) []usageStats {
		query := `SELECT agent, model, SUM(input_tokens), SUM(output_tokens), SUM(requests)
			FROM usage WHERE date >= date('now', ?)
			GROUP BY agent, model ORDER BY SUM(input_tokens)+SUM(output_tokens) DESC`
		rows, err := db.Query(query, fmt.Sprintf("-%d days", days))
		if err != nil {
			log.Printf("[usage] query error: %v", err)
			return []usageStats{}
		}
		defer rows.Close()

		var results []usageStats
		for rows.Next() {
			var s usageStats
			if err := rows.Scan(&s.Agent, &s.Model, &s.InputTokens, &s.OutputTokens, &s.Messages); err != nil {
				continue
			}
			s.TotalTokens = s.InputTokens + s.OutputTokens
			// We don't have orchestrator or duration in the usage table; default orchestrator to "inber"
			s.Orchestrator = "inber"
			results = append(results, s)
		}
		if results == nil {
			results = []usageStats{}
		}
		return results
	}

	resp := map[string]interface{}{
		"day":   queryPeriod(1),
		"week":  queryPeriod(7),
		"month": queryPeriod(30),
	}
	json.NewEncoder(w).Encode(resp)
}

// --- /api/gateway ---

func (ba *BusAgent) handleGateway(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	baseURL := envOr("OPENCLAW_URL", "http://localhost:18789")
	token := os.Getenv("OPENCLAW_TOKEN")

	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("GET", baseURL+"/health", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{"status": "down", "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Try to parse as JSON and forward; otherwise wrap
	var parsed map[string]interface{}
	if json.Unmarshal(body, &parsed) == nil {
		if _, ok := parsed["status"]; !ok {
			parsed["status"] = "ok"
		}
		json.NewEncoder(w).Encode(parsed)
	} else {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "raw": string(body)})
	}
}

// --- /api/gateway/sessions ---

func (ba *BusAgent) handleGatewaySessions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	baseURL := envOr("OPENCLAW_URL", "http://localhost:18789")
	token := os.Getenv("OPENCLAW_TOKEN")

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", baseURL+"/sessions", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// --- /api/forge/topology ---

func (ba *BusAgent) handleForgeTopology(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	type topoNode struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Port   int    `json:"port,omitempty"`
		Status string `json:"status,omitempty"`
		URL    string `json:"url,omitempty"`
	}
	type topoLink struct {
		From   string `json:"from"`
		To     string `json:"to"`
		EnvVar string `json:"env_var,omitempty"`
		Value  string `json:"value,omitempty"`
		Proto  string `json:"proto,omitempty"`
	}
	type topoEnv struct {
		Name  string     `json:"name"`
		Nodes []topoNode `json:"nodes"`
		Links []topoLink `json:"links"`
	}

	// Build prod topology from known services
	prodNodes := []topoNode{
		{ID: "prod-bus", Name: "bus", Type: "service", Port: 8100, Status: "running"},
		{ID: "prod-bus-agent", Name: "bus-agent", Type: "service", Port: 8101, Status: "running"},
		{ID: "prod-inber", Name: "inber", Type: "service", Port: 8200, Status: "running"},
		{ID: "prod-openclaw", Name: "openclaw", Type: "service", Port: 18789, Status: "running"},
		{ID: "prod-logstack", Name: "logstack", Type: "service", Port: 8088, Status: "running"},
		{ID: "prod-dash", Name: "dash", Type: "service", Port: 443, Status: "running", URL: "https://dash.kayushkin.com"},
	}

	prodLinks := []topoLink{
		{From: "prod-bus-agent", To: "prod-bus", EnvVar: "BUS_URL", Value: "http://localhost:8100", Proto: "ws"},
		{From: "prod-bus-agent", To: "prod-inber", EnvVar: "", Proto: "cli"},
		{From: "prod-bus-agent", To: "prod-openclaw", EnvVar: "OPENCLAW_URL", Value: "http://localhost:18789", Proto: "http"},
		{From: "prod-dash", To: "prod-bus-agent", EnvVar: "", Value: "/api/ → :8101", Proto: "http"},
	}

	prodEnv := topoEnv{Name: "prod", Nodes: prodNodes, Links: prodLinks}

	// Build staging envs from forge
	var staging []topoEnv
	forgeStatus := ba.forge.Status()
	for _, p := range forgeStatus {
		for _, s := range p.Slots {
			if s.Status == "acquired" {
				envName := fmt.Sprintf("env-%d", s.ID)
				nodes := []topoNode{
					{ID: fmt.Sprintf("%s-app", envName), Name: p.ID, Type: "service", Port: 3000 + s.ID, Status: "running"},
				}
				staging = append(staging, topoEnv{Name: envName, Nodes: nodes, Links: []topoLink{}})
			}
		}
	}
	if staging == nil {
		staging = []topoEnv{}
	}

	external := []topoNode{
		{ID: "ext-anthropic", Name: "Anthropic", Type: "external", URL: "https://api.anthropic.com"},
		{ID: "ext-openai", Name: "OpenAI", Type: "external", URL: "https://api.openai.com"},
		{ID: "ext-discord", Name: "Discord", Type: "external", URL: "https://discord.com"},
	}

	result := map[string]interface{}{
		"prod":     prodEnv,
		"staging":  staging,
		"external": external,
	}
	json.NewEncoder(w).Encode(result)
}

// --- /api/forge/routes ---

func (ba *BusAgent) handleForgeRoutes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusOK)
		return
	}

	// For now return static route info
	type routeInfo struct {
		Env     string   `json:"env"`
		Service string   `json:"service"`
		EnvVar  string   `json:"env_var"`
		Value   string   `json:"value"`
		Proto   string   `json:"proto"`
		Target  string   `json:"target"`
		Options []string `json:"options"`
	}

	routes := []routeInfo{
		{
			Env: "prod", Service: "bus-agent", EnvVar: "BUS_URL",
			Value: "http://localhost:8100", Proto: "ws", Target: "bus",
			Options: []string{"Local|http://localhost:8100", "Remote|https://bus.kayushkin.com"},
		},
		{
			Env: "prod", Service: "bus-agent", EnvVar: "OPENCLAW_URL",
			Value: "http://localhost:18789", Proto: "http", Target: "openclaw",
			Options: []string{"Local|http://localhost:18789"},
		},
	}

	json.NewEncoder(w).Encode(routes)
}
