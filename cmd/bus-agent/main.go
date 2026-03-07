// bus-agent subscribes to the bus for inbound messages,
// routes them to per-agent queues, dispatches to configured backends
// (CLI tools like inber/claude-code/codex, or HTTP APIs like OpenClaw),
// and publishes responses.
//
// Each agent gets its own goroutine for sequential processing.
// Different agents run concurrently.
// Spawn requests (INBER_SPAWN) are routed to the target agent's queue.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	modelstore "github.com/kayushkin/model-store"
)

// --- config ---

// Config defines backends, agent→backend mappings, and channel routing.
//
// Example config.json:
//
//	{
//	  "backends": {
//	    "inber": {
//	      "type": "cli",
//	      "cmd": ["~/bin/inber", "run", "-a", "{agent}"],
//	      "dir": "~/life/repos/inber",
//	      "stdin": "json",
//	      "features": ["meta", "spawns", "inject"]
//	    },
//	    "openclaw": {
//	      "type": "http",
//	      "url": "http://localhost:3007",
//	      "token": "gateway-token"
//	    },
//	    "claude-code": {
//	      "type": "cli",
//	      "cmd": ["claude", "--print"],
//	      "stdin": "text"
//	    }
//	  },
//	  "agents": {
//	    "browser": "openclaw",
//	    "coder": "claude-code"
//	  },
//	  "routes": [
//	    {"channel": "discord", "agent": "claxon"},
//	    {"channel": "dashboard", "agent": "brigid"}
//	  ],
//	  "default_backend": "inber",
//	  "default_agent": "claxon"
//	}
type Config struct {
	Backends       map[string]BackendConfig `json:"backends"`
	Agents         map[string]string        `json:"agents"`          // seed: agent name → orchestrator (populates DB on first run)
	Routes         []RouteConfig            `json:"routes"`          // channel → agent mapping
	DefaultBackend string                   `json:"default_backend"` // fallback backend/orchestrator
	DefaultAgent   string                   `json:"default_agent"`   // fallback agent
	RegistryDB     string                   `json:"registry_db"`     // agent registry DB path (default ~/.config/bus-agent/agents.db)
}

type RouteConfig struct {
	Channel string `json:"channel,omitempty"` // channel prefix match (empty = catch-all)
	Agent   string `json:"agent,omitempty"`   // target agent name
	Backend string `json:"backend,omitempty"` // backend override for this route
}

// --- message types ---

type busMessage struct {
	ID      int64           `json:"id"`
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload"`
	Source  string          `json:"source"`
}

type siMessage struct {
	ID           string       `json:"id,omitempty"`
	Text         string       `json:"text"`
	Author       string       `json:"author,omitempty"`
	Agent        string       `json:"agent,omitempty"`
	Orchestrator string       `json:"orchestrator,omitempty"`
	Channel      string       `json:"channel,omitempty"`
	ReplyTo      string       `json:"reply_to,omitempty"`
	MediaURL     string       `json:"media_url,omitempty"`
	Timestamp    time.Time    `json:"timestamp"`
	Meta         *messageMeta `json:"meta,omitempty"`
}

type messageMeta struct {
	InputTokens         int     `json:"input_tokens,omitempty"`
	OutputTokens        int     `json:"output_tokens,omitempty"`
	CacheReadTokens     int     `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens int     `json:"cache_creation_tokens,omitempty"`
	ToolCalls           int     `json:"tool_calls,omitempty"`
	Cost                float64 `json:"cost,omitempty"`
	DurationMs          int64   `json:"duration_ms,omitempty"`
	Model               string  `json:"model,omitempty"`
	Turn                int     `json:"turn,omitempty"`
}

type spawnRequest struct {
	Agent        string `json:"agent"`
	Orchestrator string `json:"orchestrator,omitempty"`
	Task         string `json:"task"`
}

// --- per-agent queue ---

type agentTask struct {
	msg          siMessage
	orchestrator string // explicit orchestrator (from spawn or registry)
}

type agentQueue struct {
	name string
	ch   chan agentTask

	mu      sync.Mutex
	running bool
	inject  chan siMessage // buffered channel for mid-run message injection
}

// --- bus agent ---

type BusAgent struct {
	busURL   string
	token    string
	consumer string
	config   *Config
	backends map[string]Backend
	registry *AgentRegistry
	http     *http.Client

	queues   map[string]*agentQueue
	queuesMu sync.Mutex
	ctx      context.Context
	wg       sync.WaitGroup
}

func main() {
	busURL := flag.String("bus", envOr("BUS_URL", "http://localhost:8100"), "bus URL")
	token := flag.String("token", envOr("BUS_TOKEN", ""), "bus auth token")
	consumer := flag.String("consumer", envOr("BUS_CONSUMER", "bus-agent-wsl"), "consumer ID")
	configFile := flag.String("config", envOr("BUS_AGENT_CONFIG", ""), "config file path")

	// Legacy flags — used when no config file is provided.
	inberBin := flag.String("inber", envOr("INBER_BIN", os.ExpandEnv("$HOME/bin/inber")), "inber binary path")
	inberDir := flag.String("dir", envOr("INBER_DIR", os.ExpandEnv("$HOME/life/repos/inber")), "inber working directory")
	defaultAgent := flag.String("agent", envOr("BUS_DEFAULT_AGENT", "claxon"), "default agent")
	routesFile := flag.String("routes", envOr("BUS_ROUTES", ""), "channel routes JSON file (legacy)")
	flag.Parse()

	// Load or build config.
	var cfg Config
	if *configFile != "" {
		data, err := os.ReadFile(*configFile)
		if err != nil {
			log.Fatalf("[bus-agent] failed to read config: %v", err)
		}
		// Expand ${VAR} references in config before parsing.
		data = []byte(os.ExpandEnv(string(data)))
		if err := json.Unmarshal(data, &cfg); err != nil {
			log.Fatalf("[bus-agent] failed to parse config: %v", err)
		}
		log.Printf("[bus-agent] loaded config: %d backends, %d routes", len(cfg.Backends), len(cfg.Routes))
	} else {
		// Build implicit config from legacy CLI flags.
		cfg = Config{
			Backends: map[string]BackendConfig{
				"inber": {
					Type:     "cli",
					Cmd:      []string{*inberBin, "run", "-a", "{agent}"},
					Dir:      *inberDir,
					Stdin:    "json",
					Features: []string{"meta", "spawns", "inject"},
				},
			},
			DefaultBackend: "inber",
			DefaultAgent:   *defaultAgent,
		}

		// Load legacy routes file.
		if *routesFile != "" {
			var routes []struct {
				Prefix string `json:"prefix"`
				Agent  string `json:"agent"`
			}
			data, err := os.ReadFile(*routesFile)
			if err != nil {
				log.Fatalf("[bus-agent] failed to read routes: %v", err)
			}
			if err := json.Unmarshal(data, &routes); err != nil {
				log.Fatalf("[bus-agent] failed to parse routes: %v", err)
			}
			for _, r := range routes {
				cfg.Routes = append(cfg.Routes, RouteConfig{
					Channel: r.Prefix,
					Agent:   r.Agent,
				})
			}
			log.Printf("[bus-agent] loaded %d legacy routes", len(routes))
		}
	}

	// Initialize backends.
	backends := make(map[string]Backend)
	for name, bcfg := range cfg.Backends {
		b, err := NewBackend(name, bcfg)
		if err != nil {
			log.Fatalf("[bus-agent] failed to create backend %q: %v", name, err)
		}
		backends[name] = b
		log.Printf("[bus-agent] backend %q (%s) ready", name, bcfg.Type)
	}
	if _, ok := backends[cfg.DefaultBackend]; !ok {
		log.Fatalf("[bus-agent] default backend %q not found in config", cfg.DefaultBackend)
	}

	// Open agent registry DB.
	dbPath := cfg.RegistryDB
	if dbPath == "" {
		dbPath = expandHome("~/.config/bus-agent/agents.db")
	}
	registry, err := OpenRegistry(dbPath)
	if err != nil {
		log.Fatalf("[bus-agent] failed to open agent registry: %v", err)
	}
	defer registry.Close()

	// Seed registry from config (only adds agents not already in DB).
	for name, orch := range cfg.Agents {
		if _, err := registry.Get(name, orch); err != nil {
			registry.Set(AgentEntry{Name: name, Orchestrator: orch, Enabled: true})
			log.Printf("[bus-agent] seeded agent %q → %s", name, orch)
		}
	}

	// Log registered agents.
	if agents, err := registry.List(); err == nil && len(agents) > 0 {
		log.Printf("[bus-agent] %d agents registered", len(agents))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		log.Println("[bus-agent] shutting down...")
		cancel()
	}()

	ba := &BusAgent{
		busURL:   *busURL,
		token:    *token,
		consumer: *consumer,
		config:   &cfg,
		backends: backends,
		registry: registry,
		http:     &http.Client{Timeout: 10 * time.Second},
		queues:   make(map[string]*agentQueue),
		ctx:      ctx,
	}

	go ba.serveAPI()
	ba.run()
	ba.wg.Wait()
}

// resolveAgent returns the agent name for a given channel.
func (ba *BusAgent) resolveAgent(channel string) string {
	for _, r := range ba.config.Routes {
		if r.Channel != "" && !strings.HasPrefix(channel, r.Channel) {
			continue
		}
		if r.Agent != "" {
			return r.Agent
		}
	}
	return ba.config.DefaultAgent
}

// resolveBackend returns the backend for an agent.
// Priority: explicit orchestrator → registry DB → default backend.
func (ba *BusAgent) resolveBackend(agent, orchestrator string) Backend {
	// 1. Explicit orchestrator from spawn request or message.
	if orchestrator != "" {
		if b, ok := ba.backends[orchestrator]; ok {
			return b
		}
		log.Printf("[bus-agent] unknown orchestrator %q for agent %q, falling back", orchestrator, agent)
	}

	// 2. Registry DB lookup.
	if ba.registry != nil {
		if orch, ok := ba.registry.Resolve(agent); ok {
			if b, ok := ba.backends[orch]; ok {
				return b
			}
		}
	}

	// 3. Default backend.
	return ba.backends[ba.config.DefaultBackend]
}

// getOrCreateQueue returns the queue for an agent, creating one if needed.
func (ba *BusAgent) getOrCreateQueue(name string) *agentQueue {
	ba.queuesMu.Lock()
	defer ba.queuesMu.Unlock()

	if q, ok := ba.queues[name]; ok {
		return q
	}

	q := &agentQueue{
		name: name,
		ch:   make(chan agentTask, 100),
	}
	ba.queues[name] = q

	ba.wg.Add(1)
	go ba.runQueue(q)
	log.Printf("[bus-agent] created queue for agent %q", name)

	return q
}

// runQueue processes tasks for a single agent sequentially,
// dispatching each to the appropriate backend.
func (ba *BusAgent) runQueue(q *agentQueue) {
	defer ba.wg.Done()

	for {
		select {
		case task := <-q.ch:
			backend := ba.resolveBackend(q.name, task.orchestrator)

			// Create injection channel for this run.
			inject := make(chan siMessage, 10)
			q.mu.Lock()
			q.running = true
			q.inject = inject
			q.mu.Unlock()

			resp, spawns := backend.Run(ba.ctx, q.name, task.msg, inject)

			// Clear injection state.
			q.mu.Lock()
			q.running = false
			q.inject = nil
			q.mu.Unlock()
			close(inject)

			ba.publish(resp)

			// Route spawn requests to target agent queues.
			for _, s := range spawns {
				tq := ba.getOrCreateQueue(s.Agent)
				tq.ch <- agentTask{
					msg: siMessage{
						Text:      s.Task,
						Channel:   task.msg.Channel, // inherit channel so response reaches user
						Author:    "spawn:" + q.name,
						Timestamp: time.Now(),
					},
					orchestrator: s.Orchestrator,
				}
				orch := s.Orchestrator
				if orch == "" {
					orch = "(registry)"
				}
				log.Printf("[bus-agent] spawn %s → %s@%s: %s", q.name, s.Agent, orch, truncate(s.Task, 60))
			}

		case <-ba.ctx.Done():
			return
		}
	}
}

// run connects to the bus and dispatches messages to agent queues.
func (ba *BusAgent) run() {
	for {
		select {
		case <-ba.ctx.Done():
			return
		default:
		}

		if err := ba.subscribe(); err != nil {
			log.Printf("[bus-agent] error: %v, reconnecting in 3s...", err)
			select {
			case <-ba.ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (ba *BusAgent) subscribe() error {
	wsURL := strings.Replace(ba.busURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	url := fmt.Sprintf("%s/subscribe?consumer=%s&topics=inbound&token=%s",
		wsURL, ba.consumer, ba.token)

	log.Printf("[bus-agent] connecting to %s...", ba.busURL)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	log.Printf("[bus-agent] subscribed to inbound")

	// Reset read deadline when server pings us.
	conn.SetPingHandler(func(appData string) error {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
	})

	// Client-side keepalive pings.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ba.ctx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ba.ctx.Done():
			return ba.ctx.Err()
		default:
		}

		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg busMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("[bus-agent] unmarshal error: %v", err)
			continue
		}

		var siMsg siMessage
		if err := json.Unmarshal(msg.Payload, &siMsg); err != nil {
			log.Printf("[bus-agent] payload unmarshal error: %v", err)
			continue
		}

		log.Printf("[bus-agent] ← [%s] %s: %s",
			siMsg.Channel, siMsg.Author, truncate(siMsg.Text, 80))

		// Route to agent queue, or inject into running process.
		// Message-level agent takes priority over channel-based routing.
		agentName := siMsg.Agent
		if agentName == "" {
			agentName = ba.resolveAgent(siMsg.Channel)
		}
		q := ba.getOrCreateQueue(agentName)

		injected := false
		q.mu.Lock()
		if q.running && q.inject != nil {
			select {
			case q.inject <- siMsg:
				injected = true
				log.Printf("[bus-agent] injected into running %s: %s",
					agentName, truncate(siMsg.Text, 60))
			default:
				// Injection buffer full — queue as new task.
			}
		}
		q.mu.Unlock()

		if !injected {
			q.ch <- agentTask{msg: siMsg, orchestrator: siMsg.Orchestrator}
		}

		ba.ack(msg.Topic, msg.ID)
	}
}

func (ba *BusAgent) publish(msg siMessage) {
	payload, _ := json.Marshal(msg)
	body := map[string]interface{}{
		"topic":   "outbound",
		"payload": json.RawMessage(payload),
		"source":  "bus-agent",
	}
	data, _ := json.Marshal(body)

	url := ba.busURL + "/publish?token=" + ba.token
	resp, err := ba.http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("[bus-agent] publish error: %v", err)
		return
	}
	resp.Body.Close()
}

func (ba *BusAgent) ack(topic string, id int64) {
	body := map[string]interface{}{
		"consumer":   ba.consumer,
		"topic":      topic,
		"message_id": id,
	}
	data, _ := json.Marshal(body)
	url := ba.busURL + "/ack?token=" + ba.token
	resp, err := ba.http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("[bus-agent] ack error: %v", err)
		return
	}
	resp.Body.Close()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// --- API server (model status dashboard) ---

func (ba *BusAgent) serveAPI() {
	port := envOr("BUS_AGENT_API_PORT", "8101")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/agents", ba.handleAgents)
	mux.HandleFunc("/api/models/test", ba.handleModelTest)
	mux.HandleFunc("/api/models/toggle", ba.handleModelToggle)
	mux.HandleFunc("/api/models", ba.handleModelsStatus)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	log.Printf("[bus-agent] API server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Printf("[bus-agent] API server error: %v", err)
	}
}

func (ba *BusAgent) handleModelTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		http.Error(w, `{"error":"model required"}`, http.StatusBadRequest)
		return
	}

	// Find the default CLI backend's binary for model testing.
	var inberBin, inberDir string
	if bcfg, ok := ba.config.Backends[ba.config.DefaultBackend]; ok && bcfg.Type == "cli" && len(bcfg.Cmd) > 0 {
		inberBin = expandHome(bcfg.Cmd[0])
		inberDir = expandHome(bcfg.Dir)
	} else {
		http.Error(w, `{"error":"no CLI backend for model testing"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, inberBin, "run", "--raw", "--no-tools", "--new", "--detach", "-m", req.Model, "Reply with just: ok")
	cmd.Dir = inberDir
	cmd.Env = os.Environ()

	start := time.Now()
	output, err := cmd.CombinedOutput()
	durationMs := time.Since(start).Milliseconds()

	result := map[string]interface{}{
		"model":       req.Model,
		"duration_ms": durationMs,
	}

	if err != nil {
		result["status"] = "error"
		errText := string(output)
		if len(errText) > 200 {
			errText = errText[len(errText)-200:]
		}
		result["error"] = errText
	} else {
		result["status"] = "ok"
		text := strings.TrimSpace(string(output))
		if len(text) > 100 {
			text = text[:100]
		}
		result["response"] = text
	}

	json.NewEncoder(w).Encode(result)
}

func (ba *BusAgent) handleModelToggle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Model   string `json:"model"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		http.Error(w, `{"error":"model required"}`, http.StatusBadRequest)
		return
	}

	store, err := modelstore.Open("")
	if err != nil {
		http.Error(w, `{"error":"failed to open model store"}`, http.StatusInternalServerError)
		return
	}
	defer store.Close()

	if err := store.SetEnabled(req.Model, req.Enabled); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "model": req.Model, "enabled": req.Enabled})
}

func (ba *BusAgent) handleAgents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		agents, err := ba.registry.List()
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		if agents == nil {
			agents = []AgentEntry{}
		}
		json.NewEncoder(w).Encode(agents)

	case http.MethodPost:
		var entry AgentEntry
		if err := json.NewDecoder(r.Body).Decode(&entry); err != nil || entry.Name == "" || entry.Orchestrator == "" {
			http.Error(w, `{"error":"name and orchestrator required"}`, http.StatusBadRequest)
			return
		}
		// Validate orchestrator exists as a configured backend.
		if _, ok := ba.backends[entry.Orchestrator]; !ok {
			http.Error(w, fmt.Sprintf(`{"error":"unknown orchestrator %q"}`, entry.Orchestrator), http.StatusBadRequest)
			return
		}
		if err := ba.registry.Set(entry); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		log.Printf("[bus-agent] registered agent %q → %s", entry.Name, entry.Orchestrator)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "agent": entry})

	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		orchestrator := r.URL.Query().Get("orchestrator")
		if name == "" || orchestrator == "" {
			http.Error(w, `{"error":"name and orchestrator query params required"}`, http.StatusBadRequest)
			return
		}
		if err := ba.registry.Delete(name, orchestrator); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusNotFound)
			return
		}
		log.Printf("[bus-agent] removed agent %q/%s", name, orchestrator)
		json.NewEncoder(w).Encode(map[string]string{"ok": "true", "deleted": name, "orchestrator": orchestrator})

	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

func (ba *BusAgent) handleModelsStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	store, err := modelstore.Open("")
	if err != nil {
		http.Error(w, `{"error":"failed to open model store"}`, http.StatusInternalServerError)
		return
	}
	defer store.Close()

	statuses, err := store.AllModelsWithStatus()
	if err != nil {
		http.Error(w, `{"error":"failed to query models"}`, http.StatusInternalServerError)
		return
	}

	json.NewEncoder(w).Encode(statuses)
}
