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
	_ "github.com/mattn/go-sqlite3"
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
	Stream       string       `json:"stream,omitempty"`    // "delta" or "done"
	StreamID     string       `json:"stream_id,omitempty"` // groups deltas with their final message
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
	target       AgentTarget // resolved (name, orchestrator) pair
}

type agentQueue struct {
	target AgentTarget
	ch     chan agentTask

	mu      sync.Mutex
	running bool
	inject  chan siMessage // buffered channel for mid-run message injection
}

// --- bus agent ---

type BusAgent struct {
	busURL        string
	token         string
	consumer      string
	backends      map[string]Backend
	registry      *AgentRegistry
	http          *http.Client
	defaultTarget AgentTarget

	queues   map[QueueKey]*agentQueue
	queuesMu sync.Mutex
	ctx      context.Context
	wg       sync.WaitGroup
}

func main() {
	busURL := flag.String("bus", envOr("BUS_URL", "http://localhost:8100"), "bus URL")
	token := flag.String("token", envOr("BUS_TOKEN", ""), "bus auth token")
	consumer := flag.String("consumer", envOr("BUS_CONSUMER", "bus-agent-wsl"), "consumer ID")
	dbPath := flag.String("db", envOr("BUS_AGENT_DB", "~/.config/bus-agent/agents.db"), "registry database path")
	syncAgents := flag.Bool("sync", false, "sync agents from backends on startup")
	syncInterval := flag.Duration("sync-interval", 0, "periodic sync interval (e.g., '10m'), 0=disabled")
	flag.Parse()

	// Open registry DB (single source of truth).
	registry, err := OpenRegistry(expandHome(*dbPath))
	if err != nil {
		log.Fatalf("[bus-agent] failed to open registry: %v", err)
	}
	defer registry.Close()

	// Sync agents from backends if requested
	if *syncAgents || *syncInterval > 0 {
		added, updated, removed, err := registry.SyncAllAgents()
		if err != nil {
			log.Printf("[bus-agent] agent sync error: %v", err)
		} else if added+updated+removed > 0 {
			log.Printf("[bus-agent] synced agents: +%d, ~%d, -%d", added, updated, removed)
		}
	}

	// Periodic sync
	if *syncInterval > 0 {
		go func() {
			ticker := time.NewTicker(*syncInterval)
			defer ticker.Stop()
			for range ticker.C {
				added, updated, removed, err := registry.SyncAllAgents()
				if err != nil {
					log.Printf("[bus-agent] periodic sync error: %v", err)
				} else if added+updated+removed > 0 {
					log.Printf("[bus-agent] periodic sync: +%d, ~%d, -%d", added, updated, removed)
				}
			}
		}()
	}

	// Load backends from DB.
	backendEntries, err := registry.ListBackends()
	if err != nil {
		log.Fatalf("[bus-agent] failed to load backends: %v", err)
	}
	backends := make(map[string]Backend)
	for _, be := range backendEntries {
		b, err := NewBackend(be.Name, be.Config)
		if err != nil {
			log.Printf("[bus-agent] warning: failed to create backend %q: %v", be.Name, err)
			continue
		}
		backends[be.Name] = b
		log.Printf("[bus-agent] backend %q (%s) ready", be.Name, be.Type)
	}
	if len(backends) == 0 {
		log.Fatalf("[bus-agent] no backends configured in DB")
	}

	// Load agents and routes from DB.
	agents, err := registry.ListAgents()
	if err != nil {
		log.Fatalf("[bus-agent] failed to load agents: %v", err)
	}
	log.Printf("[bus-agent] %d agents registered", len(agents))

	routes, err := registry.ListRoutes()
	if err != nil {
		log.Printf("[bus-agent] warning: failed to load routes: %v", err)
	}
	log.Printf("[bus-agent] %d routes configured", len(routes))

	// Find default agent target (first enabled agent, or claxon/inber).
	defaultTarget := AgentTarget{Name: "claxon", Orchestrator: "inber"}
	for _, a := range agents {
		if a.Enabled {
			defaultTarget = AgentTarget{Name: a.Name, Orchestrator: a.Orchestrator}
			break
		}
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
		busURL:        *busURL,
		token:         *token,
		consumer:      *consumer,
		backends:      backends,
		registry:      registry,
		http:          &http.Client{Timeout: 10 * time.Second},
		defaultTarget: defaultTarget,
		queues:        make(map[QueueKey]*agentQueue),
		ctx:           ctx,
	}

	go ba.serveAPI()
	ba.run()
	ba.wg.Wait()
}

// resolveTarget returns the agent target for a channel from the routes table.
func (ba *BusAgent) resolveTarget(channel string) AgentTarget {
	if target, ok := ba.registry.ResolveRoute(channel); ok {
		return target
	}
	return ba.defaultTarget
}

// getBackend returns the backend for a given orchestrator name.
func (ba *BusAgent) getBackend(orchestrator string) Backend {
	if b, ok := ba.backends[orchestrator]; ok {
		return b
	}
	log.Printf("[bus-agent] unknown orchestrator %q", orchestrator)
	return nil
}

// getOrCreateQueue returns the queue for an agent target, creating one if needed.
func (ba *BusAgent) getOrCreateQueue(target AgentTarget) *agentQueue {
	key := QueueKey{Name: target.Name, Orchestrator: target.Orchestrator}

	ba.queuesMu.Lock()
	defer ba.queuesMu.Unlock()

	if q, ok := ba.queues[key]; ok {
		return q
	}

	q := &agentQueue{
		target: target,
		ch:     make(chan agentTask, 100),
	}
	ba.queues[key] = q

	ba.wg.Add(1)
	go ba.runQueue(q)
	log.Printf("[bus-agent] created queue: %s/%s", target.Name, target.Orchestrator)

	return q
}

// runQueue processes tasks for a single agent sequentially.
func (ba *BusAgent) runQueue(q *agentQueue) {
	defer ba.wg.Done()

	for {
		select {
		case task := <-q.ch:
			log.Printf("[bus-agent] %s/%s: dequeued (text=%q)",
				task.target.Name, task.target.Orchestrator, truncate(task.msg.Text, 60))

			backend := ba.getBackend(task.target.Orchestrator)
			if backend == nil {
				log.Printf("[bus-agent] %s/%s: no backend!", task.target.Name, task.target.Orchestrator)
				continue
			}

			// Create injection channel for this run.
			inject := make(chan siMessage, 10)
			q.mu.Lock()
			q.running = true
			q.inject = inject
			q.mu.Unlock()

			// Stream callback: publish deltas to bus in real-time.
			var onStream StreamFunc
			onStream = func(delta siMessage) {
				ba.publish(delta)
			}

			resp, spawns := backend.Run(ba.ctx, task.target.Name, task.msg, inject, onStream)

			// Clear injection state and re-queue any unread injected messages.
			q.mu.Lock()
			q.running = false
			q.inject = nil
			q.mu.Unlock()
			close(inject)

			// Drain unread injected messages back to the queue so they aren't lost.
			for leftover := range inject {
				log.Printf("[bus-agent] re-queuing unread injection for %s/%s: %s",
					task.target.Name, task.target.Orchestrator, truncate(leftover.Text, 60))
				q.ch <- agentTask{msg: leftover, target: task.target}
			}

			ba.publish(resp)

			// Route spawn requests to target agent queues.
			for _, s := range spawns {
				s.Agent = strings.ToLower(s.Agent)
				spawnOrch := s.Orchestrator
				if spawnOrch == "" {
					// Look up from registry; if ambiguous, use parent's orchestrator.
					if a, ok := ba.registry.FindAgent(s.Agent); ok {
						spawnOrch = a.Orchestrator
					} else {
						spawnOrch = task.target.Orchestrator
					}
				}
				spawnTarget := AgentTarget{Name: s.Agent, Orchestrator: spawnOrch}
				tq := ba.getOrCreateQueue(spawnTarget)
				tq.ch <- agentTask{
					msg: siMessage{
						Text:      s.Task,
						Channel:   task.msg.Channel,
						Author:    "spawn:" + task.target.Name,
						Timestamp: time.Now(),
					},
					target: spawnTarget,
				}
				log.Printf("[bus-agent] spawn %s → %s/%s: %s",
					task.target.Name, s.Agent, spawnOrch, truncate(s.Task, 60))
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

		// Resolve target: (agent name, orchestrator) pair.
		var target AgentTarget
		if siMsg.Agent != "" && siMsg.Orchestrator != "" {
			// Both specified — use directly.
			target = AgentTarget{Name: siMsg.Agent, Orchestrator: siMsg.Orchestrator}
		} else if siMsg.Agent != "" {
			// Agent name only — look up in registry.
			if a, ok := ba.registry.FindAgent(siMsg.Agent); ok {
				target = AgentTarget{Name: a.Name, Orchestrator: a.Orchestrator}
			} else {
				// Unknown agent, use default orchestrator.
				target = AgentTarget{Name: siMsg.Agent, Orchestrator: ba.defaultTarget.Orchestrator}
			}
		} else {
			// No agent specified — use channel route.
			target = ba.resolveTarget(siMsg.Channel)
		}

		q := ba.getOrCreateQueue(target)

		injected := false
		q.mu.Lock()
		if q.running && q.inject != nil {
			select {
			case q.inject <- siMsg:
				injected = true
				log.Printf("[bus-agent] injected into %s/%s: %s",
					target.Name, target.Orchestrator, truncate(siMsg.Text, 60))
			default:
			}
		}
		q.mu.Unlock()

		if !injected {
			q.ch <- agentTask{msg: siMsg, target: target}
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
	mux.HandleFunc("/api/credentials/toggle", ba.handleCredentialToggle)
	mux.HandleFunc("/api/credentials", ba.handleCredentials)
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
	for _, be := range []string{"inber", ba.defaultTarget.Orchestrator} {
		if bcfg, err := ba.registry.GetBackend(be); err == nil && bcfg.Type == "cli" && len(bcfg.Config.Cmd) > 0 {
			inberBin = expandHome(bcfg.Config.Cmd[0])
			inberDir = expandHome(bcfg.Config.Dir)
			break
		}
	}
	if inberBin == "" {
		// Fallback: try any CLI backend
		backends, _ := ba.registry.ListBackends()
		for _, be := range backends {
			if be.Type == "cli" && len(be.Config.Cmd) > 0 {
				inberBin = expandHome(be.Config.Cmd[0])
				inberDir = expandHome(be.Config.Dir)
				break
			}
		}
	}
	if inberBin == "" {
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
		agents, err := ba.registry.ListAgents()
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
		if err := ba.registry.SetAgent(entry); err != nil {
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
		if err := ba.registry.DeleteAgent(name, orchestrator); err != nil {
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

	// Build response with per-model credential info
	type credRef struct {
		ID           string `json:"id"`
		Label        string `json:"label"`
		AuthType     string `json:"auth_type"`
		APIKeyMasked string `json:"api_key_masked,omitempty"`
		TokenMasked  string `json:"token_masked,omitempty"`
		Priority     int    `json:"priority"`
		Enabled      bool   `json:"enabled"`
		LastUsedAt   int64  `json:"last_used_at"`
		ErrorCount   int    `json:"error_count"`
		ExpiresAt    int64  `json:"expires_at"`
	}

	type modelWithCreds struct {
		modelstore.ModelStatus
		Credentials []credRef `json:"credentials"`
	}

	var result []modelWithCreds
	for _, ms := range statuses {
		mc := modelWithCreds{ModelStatus: ms}

		creds, err := store.CredentialsForModel(ms.ID)
		if err == nil && len(creds) > 0 {
			for _, c := range creds {
				cr := credRef{
					ID:         c.ID,
					Label:      c.Label,
					AuthType:   c.AuthType,
					Priority:   c.Priority,
					Enabled:    c.Enabled,
					LastUsedAt: c.LastUsedAt,
					ErrorCount: c.ErrorCount,
					ExpiresAt:  c.ExpiresAt,
				}
				if c.APIKey != "" {
					cr.APIKeyMasked = maskKey(c.APIKey)
				}
				if c.Token != "" {
					cr.TokenMasked = maskKey(c.Token)
				}
				mc.Credentials = append(mc.Credentials, cr)
			}
		}
		if mc.Credentials == nil {
			mc.Credentials = []credRef{}
		}
		result = append(result, mc)
	}

	json.NewEncoder(w).Encode(result)
}

// maskKey masks sensitive key/token values for display
func maskKey(key string) string {
	if len(key) <= 16 {
		return strings.Repeat("*", len(key))
	}
	return key[:8] + "..." + key[len(key)-4:]
}

func (ba *BusAgent) handleCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	store, err := modelstore.Open("")
	if err != nil {
		http.Error(w, `{"error":"failed to open model store"}`, http.StatusInternalServerError)
		return
	}
	defer store.Close()

	creds, err := store.ListCredentials("")
	if err != nil {
		http.Error(w, `{"error":"failed to query credentials"}`, http.StatusInternalServerError)
		return
	}

	// Mask sensitive fields before sending
	type credResponse struct {
		ID           string `json:"id"`
		Provider     string `json:"provider"`
		Label        string `json:"label"`
		AuthType     string `json:"auth_type"`
		APIKeyMasked string `json:"api_key_masked,omitempty"`
		TokenMasked  string `json:"token_masked,omitempty"`
		BaseURL      string `json:"base_url,omitempty"`
		Priority     int    `json:"priority"`
		Enabled      bool   `json:"enabled"`
		LastUsedAt   int64  `json:"last_used_at"`
		LastError    string `json:"last_error,omitempty"`
		LastErrorAt  int64  `json:"last_error_at"`
		ErrorCount   int    `json:"error_count"`
		ExpiresAt    int64  `json:"expires_at"`
		CreatedAt    string `json:"created_at"`
	}

	var response []credResponse
	for _, c := range creds {
		cr := credResponse{
			ID:          c.ID,
			Provider:    c.Provider,
			Label:       c.Label,
			AuthType:    c.AuthType,
			BaseURL:     c.BaseURL,
			Priority:    c.Priority,
			Enabled:     c.Enabled,
			LastUsedAt:  c.LastUsedAt,
			LastError:   c.LastError,
			LastErrorAt: c.LastErrorAt,
			ErrorCount:  c.ErrorCount,
			ExpiresAt:   c.ExpiresAt,
			CreatedAt:   c.CreatedAt,
		}
		if c.APIKey != "" {
			cr.APIKeyMasked = maskKey(c.APIKey)
		}
		if c.Token != "" {
			cr.TokenMasked = maskKey(c.Token)
		}
		response = append(response, cr)
	}

	if response == nil {
		response = []credResponse{}
	}
	json.NewEncoder(w).Encode(response)
}

func (ba *BusAgent) handleCredentialToggle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		http.Error(w, `{"error":"id required"}`, http.StatusBadRequest)
		return
	}

	store, err := modelstore.Open("")
	if err != nil {
		http.Error(w, `{"error":"failed to open model store"}`, http.StatusInternalServerError)
		return
	}
	defer store.Close()
	store.EnableAuthProfileSync("")

	if err := store.SetCredentialEnabled(req.ID, req.Enabled); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "id": req.ID, "enabled": req.Enabled})
}
