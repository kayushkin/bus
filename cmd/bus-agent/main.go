// bus-agent subscribes to the bus for inbound messages,
// routes them to per-agent queues, calls inber, and publishes responses.
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
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// --- message types ---

type busMessage struct {
	ID      int64           `json:"id"`
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload"`
	Source  string          `json:"source"`
}

type siMessage struct {
	ID        string       `json:"id,omitempty"`
	Text      string       `json:"text"`
	Author    string       `json:"author,omitempty"`
	Agent     string       `json:"agent,omitempty"`
	Channel   string       `json:"channel,omitempty"`
	ReplyTo   string       `json:"reply_to,omitempty"`
	MediaURL  string       `json:"media_url,omitempty"`
	Timestamp time.Time    `json:"timestamp"`
	Meta      *messageMeta `json:"meta,omitempty"`
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

type channelRoute struct {
	Prefix string `json:"prefix"`
	Agent  string `json:"agent"`
}

type spawnRequest struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

// --- per-agent queue ---

type agentTask struct {
	msg siMessage
}

type agentQueue struct {
	name string
	ch   chan agentTask
}

// --- bus agent ---

type BusAgent struct {
	busURL       string
	token        string
	consumer     string
	inberBin     string
	inberDir     string
	defaultAgent string
	routes       []channelRoute
	http         *http.Client

	queues   map[string]*agentQueue
	queuesMu sync.Mutex
	ctx      context.Context
	wg       sync.WaitGroup
}

func main() {
	busURL := flag.String("bus", envOr("BUS_URL", "http://localhost:8100"), "bus URL")
	token := flag.String("token", envOr("BUS_TOKEN", ""), "bus auth token")
	consumer := flag.String("consumer", envOr("BUS_CONSUMER", "bus-agent-wsl"), "consumer ID")
	inberBin := flag.String("inber", envOr("INBER_BIN", os.ExpandEnv("$HOME/bin/inber")), "inber binary path")
	inberDir := flag.String("dir", envOr("INBER_DIR", os.ExpandEnv("$HOME/life/repos/inber")), "inber working directory")
	defaultAgent := flag.String("agent", envOr("BUS_DEFAULT_AGENT", "claxon"), "default agent")
	routesFile := flag.String("routes", envOr("BUS_ROUTES", ""), "channel routes JSON file")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
		log.Println("[bus-agent] shutting down...")
		cancel()
	}()

	var routes []channelRoute
	if *routesFile != "" {
		data, err := os.ReadFile(*routesFile)
		if err != nil {
			log.Fatalf("[bus-agent] failed to read routes file: %v", err)
		}
		if err := json.Unmarshal(data, &routes); err != nil {
			log.Fatalf("[bus-agent] failed to parse routes: %v", err)
		}
		log.Printf("[bus-agent] loaded %d channel routes", len(routes))
	}

	ba := &BusAgent{
		busURL:       *busURL,
		token:        *token,
		consumer:     *consumer,
		inberBin:     *inberBin,
		inberDir:     *inberDir,
		defaultAgent: *defaultAgent,
		routes:       routes,
		http:         &http.Client{Timeout: 10 * time.Second},
		queues:       make(map[string]*agentQueue),
		ctx:          ctx,
	}

	ba.run()
	ba.wg.Wait()
}

// resolveAgent maps a channel to an agent name using configured routes.
func (ba *BusAgent) resolveAgent(channel string) string {
	for _, r := range ba.routes {
		if strings.HasPrefix(channel, r.Prefix) || channel == r.Prefix {
			return r.Agent
		}
	}
	return ba.defaultAgent
}

// getOrCreateQueue returns the queue for an agent, creating one if needed.
// Each queue gets its own goroutine for sequential processing.
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

// runQueue processes tasks for a single agent, one at a time.
func (ba *BusAgent) runQueue(q *agentQueue) {
	defer ba.wg.Done()

	for {
		select {
		case task := <-q.ch:
			resp, spawns := ba.processWithInber(q.name, task.msg)
			ba.publish(resp)

			// Route spawn requests to target agent queues
			for _, s := range spawns {
				tq := ba.getOrCreateQueue(s.Agent)
				tq.ch <- agentTask{
					msg: siMessage{
						Text:      s.Task,
						Channel:   task.msg.Channel, // inherit channel so response reaches user
						Author:    "spawn:" + q.name,
						Timestamp: time.Now(),
					},
				}
				log.Printf("[bus-agent] spawn %s → %s: %s", q.name, s.Agent, truncate(s.Task, 60))
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

		// Route to agent queue (non-blocking dispatch)
		agentName := ba.resolveAgent(siMsg.Channel)
		q := ba.getOrCreateQueue(agentName)
		q.ch <- agentTask{msg: siMsg}

		// Ack immediately — message is queued for processing
		ba.ack(msg.Topic, msg.ID)
	}
}

// processWithInber runs inber for a single message and returns the response
// plus any spawn requests found in stderr.
func (ba *BusAgent) processWithInber(agentName string, msg siMessage) (siMessage, []spawnRequest) {
	args := []string{"run", "-a", agentName}

	input := msg.Text
	if msg.Author != "" {
		input = fmt.Sprintf("[%s] %s", msg.Author, msg.Text)
	}

	cmdCtx, cancel := context.WithTimeout(ba.ctx, 10*time.Minute)
	defer cancel()

	start := time.Now()

	cmd := exec.CommandContext(cmdCtx, ba.inberBin, args...)
	cmd.Dir = ba.inberDir
	cmd.Env = append(os.Environ(), "INBER_BUS_SPAWN=1") // signal spawn tool to emit INBER_SPAWN

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return siMessage{Text: "error: " + err.Error(), Channel: msg.Channel, Timestamp: time.Now()}, nil
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return siMessage{Text: "error: " + err.Error(), Channel: msg.Channel, Timestamp: time.Now()}, nil
	}

	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return siMessage{Text: "error starting inber: " + err.Error(), Channel: msg.Channel, Timestamp: time.Now()}, nil
	}

	stdin.Write([]byte(input))
	stdin.Close()

	output, _ := io.ReadAll(stdout)
	errData, _ := io.ReadAll(stderr)
	cmd.Wait()

	duration := time.Since(start)
	stderrStr := string(errData)

	text := strings.TrimSpace(string(output))
	if text == "" && len(errData) > 0 {
		text = strings.TrimSpace(stderrStr)
	}

	meta := parseInberMeta(stderrStr, duration, agentName)
	spawns := parseInberSpawns(stderrStr)

	log.Printf("[bus-agent] → [%s] %s: %s (%.1fs)", msg.Channel, agentName, truncate(text, 80), duration.Seconds())

	resp := siMessage{
		Text:      text,
		Channel:   msg.Channel,
		Author:    agentName,
		Timestamp: time.Now(),
		Meta:      meta,
	}

	return resp, spawns
}

// parseInberSpawns extracts INBER_SPAWN:{...} lines from stderr.
func parseInberSpawns(stderr string) []spawnRequest {
	var spawns []spawnRequest
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "INBER_SPAWN:") {
			continue
		}
		jsonStr := strings.TrimPrefix(line, "INBER_SPAWN:")
		var s spawnRequest
		if err := json.Unmarshal([]byte(jsonStr), &s); err != nil {
			log.Printf("[bus-agent] failed to parse INBER_SPAWN: %v", err)
			continue
		}
		if s.Agent != "" && s.Task != "" {
			spawns = append(spawns, s)
		}
	}
	return spawns
}

// parseInberMeta extracts INBER_META:{...} from stderr.
func parseInberMeta(stderr string, duration time.Duration, model string) *messageMeta {
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "INBER_META:") {
			jsonStr := strings.TrimPrefix(line, "INBER_META:")
			var meta messageMeta
			if err := json.Unmarshal([]byte(jsonStr), &meta); err == nil {
				meta.DurationMs = duration.Milliseconds()
				return &meta
			}
		}
	}

	// Fallback: parse box-drawing format
	meta := &messageMeta{
		DurationMs: duration.Milliseconds(),
		Model:      model,
	}

	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "│ in=") {
			fmt.Sscanf(line, "│ in=%d out=%d total=%*d tools=%d",
				&meta.InputTokens, &meta.OutputTokens, &meta.ToolCalls)
		}
		if strings.Contains(line, "│ cache:") {
			fmt.Sscanf(line, "│ cache: %d read, %d created",
				&meta.CacheReadTokens, &meta.CacheCreationTokens)
		}
		if strings.HasPrefix(line, "│ cost=") {
			fmt.Sscanf(line, "│ cost=$%f", &meta.Cost)
		}
		if strings.HasPrefix(line, "model:") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				meta.Model = parts[1]
			}
		}
	}

	if meta.InputTokens > 0 || meta.OutputTokens > 0 || meta.Cost > 0 {
		return meta
	}
	return nil
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
