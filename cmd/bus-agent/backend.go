package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// StreamFunc is called with each streaming delta chunk. If nil, no streaming.
type StreamFunc func(delta siMessage)

// Backend executes agent tasks.
type Backend interface {
	Run(ctx context.Context, agent string, msg siMessage, inject <-chan siMessage, onStream StreamFunc) (siMessage, []spawnRequest)
}

// BackendConfig defines a backend in the config file.
type BackendConfig struct {
	Type        string   `json:"type"`                  // "cli", "http", or "openai"
	Cmd         []string `json:"cmd,omitempty"`          // CLI: command template ({agent} replaced)
	Dir         string   `json:"dir,omitempty"`          // CLI: working directory ({agent} replaced)
	Env         []string `json:"env,omitempty"`          // CLI: extra KEY=VALUE env vars
	Stdin       string   `json:"stdin,omitempty"`        // CLI: "json" or "text" (default "text")
	Features    []string `json:"features,omitempty"`     // CLI: ["meta", "spawns", "inject"]
	Timeout     string   `json:"timeout,omitempty"`      // max run duration (default "10m")
	URL         string   `json:"url,omitempty"`          // HTTP/OpenAI: base URL
	Token       string   `json:"token,omitempty"`        // HTTP/OpenAI: auth token
	Model       string   `json:"model,omitempty"`        // OpenAI: model field (default "openclaw")
	AgentHeader string            `json:"agent_header,omitempty"` // OpenAI: header for agent routing (e.g. "x-openclaw-agent-id")
	AgentMap    map[string]string `json:"agent_map,omitempty"`    // OpenAI: bus name → backend agent ID (e.g. "claxon" → "main")
	SessionKey  string            `json:"session_key,omitempty"`  // OpenAI: session key template. {agent} = mapped agent ID. Sent as x-openclaw-session-key.
}

// NewBackend creates a Backend from config.
func NewBackend(name string, cfg BackendConfig) (Backend, error) {
	timeout := 10 * time.Minute
	if cfg.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Timeout); err == nil {
			timeout = d
		}
	}

	switch cfg.Type {
	case "cli":
		features := make(map[string]bool)
		for _, f := range cfg.Features {
			features[f] = true
		}
		return &CLIBackend{
			name:    name,
			cmd:     cfg.Cmd,
			dir:     cfg.Dir,
			env:     cfg.Env,
			stdin:   cfg.Stdin,
			meta:    features["meta"],
			spawns:  features["spawns"],
			inject:  features["inject"],
			timeout: timeout,
		}, nil

	case "http":
		return &HTTPBackend{
			name:    name,
			url:     cfg.URL,
			token:   cfg.Token,
			timeout: timeout,
			client:  &http.Client{Timeout: timeout},
		}, nil

	case "openai":
		model := cfg.Model
		if model == "" {
			model = "openclaw"
		}
		return &OpenAIBackend{
			name:           name,
			url:            cfg.URL,
			token:          cfg.Token,
			model:          model,
			agentHeader:    cfg.AgentHeader,
			agentMap:       cfg.AgentMap,
			sessionKeyTmpl: cfg.SessionKey,
			timeout:        timeout,
			client:         &http.Client{Timeout: timeout},
		}, nil

	default:
		return nil, fmt.Errorf("unknown backend type: %q", cfg.Type)
	}
}

// --- CLI Backend ---
// Runs an external command (inber, claude-code, codex, etc.).
// Supports {agent} placeholder in cmd and dir.

type CLIBackend struct {
	name    string
	cmd     []string
	dir     string
	env     []string
	stdin   string // "json" or "text"
	meta    bool   // parse INBER_META from stderr
	spawns  bool   // parse INBER_SPAWN from stderr
	inject  bool   // keep stdin open for mid-run injection
	timeout time.Duration
}

func (b *CLIBackend) Run(ctx context.Context, agent string, msg siMessage, injectCh <-chan siMessage, _ StreamFunc) (siMessage, []spawnRequest) {
	// Build command with {agent} placeholder replacement.
	args := make([]string, len(b.cmd))
	for i, a := range b.cmd {
		args[i] = replaceVars(a, agent)
	}
	dir := replaceVars(b.dir, agent)

	cmdCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(cmdCtx, expandHome(args[0]), args[1:]...)
	if dir != "" {
		cmd.Dir = expandHome(dir)
	}
	cmd.Env = append(os.Environ(), b.env...)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return errResp(msg, err), nil
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errResp(msg, err), nil
	}
	stderrPipe, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return errResp(msg, fmt.Errorf("start %s: %w", args[0], err)), nil
	}

	// Write initial message.
	input := formatInput(msg)
	if b.stdin == "json" {
		writeJSON(stdinPipe, input, msg.Author)
	} else {
		stdinPipe.Write([]byte(input + "\n"))
	}

	// Handle mid-run injection (keeps stdin open for additional messages).
	if b.inject && injectCh != nil {
		go func() {
			for m := range injectCh {
				text := formatInput(m)
				if b.stdin == "json" {
					if err := writeJSON(stdinPipe, text, m.Author); err != nil {
						return
					}
				} else {
					if _, err := stdinPipe.Write([]byte(text + "\n")); err != nil {
						return
					}
				}
				log.Printf("[%s] injected: %s", b.name, truncate(text, 60))
			}
		}()
	} else {
		stdinPipe.Close()
	}

	rawOutput, _ := io.ReadAll(stdout)
	output := stripANSI(rawOutput)
	errData, _ := io.ReadAll(stderrPipe)

	if b.inject {
		stdinPipe.Close()
	}
	cmd.Wait()

	duration := time.Since(start)
	stderrStr := string(errData)

	text := strings.TrimSpace(string(output))
	if text == "" && stderrStr != "" {
		text = strings.TrimSpace(stderrStr)
	}

	var parsedMeta *messageMeta
	if b.meta {
		parsedMeta = parseInberMeta(stderrStr, duration, agent)
	}

	var parsedSpawns []spawnRequest
	if b.spawns {
		parsedSpawns = parseInberSpawns(stderrStr)
	}

	log.Printf("[%s] → [%s] %s: %s (%.1fs)",
		b.name, msg.Channel, agent, truncate(text, 80), duration.Seconds())

	return siMessage{
		Text:         text,
		Channel:      msg.Channel,
		Agent:        agent,
		Author:       agent,
		Orchestrator: b.name,
		Timestamp:    time.Now(),
		Meta:         parsedMeta,
	}, parsedSpawns
}

// --- HTTP Backend ---
// Calls a remote agent API (OpenClaw, or any service implementing the /api/run convention).
// POST /api/run with {text, agent, channel, author} → {text, meta}.

type HTTPBackend struct {
	name    string
	url     string
	token   string
	timeout time.Duration
	client  *http.Client
}

func (b *HTTPBackend) Run(ctx context.Context, agent string, msg siMessage, _ <-chan siMessage, _ StreamFunc) (siMessage, []spawnRequest) {
	reqBody := struct {
		Text    string `json:"text"`
		Agent   string `json:"agent"`
		Channel string `json:"channel"`
		Author  string `json:"author,omitempty"`
	}{
		Text:    msg.Text,
		Agent:   agent,
		Channel: msg.Channel,
		Author:  msg.Author,
	}
	data, _ := json.Marshal(reqBody)

	url := strings.TrimRight(b.url, "/") + "/api/run"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return errResp(msg, err), nil
	}
	req.Header.Set("Content-Type", "application/json")
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}

	start := time.Now()
	resp, err := b.client.Do(req)
	if err != nil {
		return errResp(msg, err), nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	duration := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		return errResp(msg, fmt.Errorf("http %d: %s", resp.StatusCode,
			truncate(string(body), 200))), nil
	}

	var result struct {
		Text string       `json:"text"`
		Meta *messageMeta `json:"meta,omitempty"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		// Treat raw body as text response.
		result.Text = strings.TrimSpace(string(body))
	}

	log.Printf("[%s] → [%s] %s: %s (%.1fs)",
		b.name, msg.Channel, agent, truncate(result.Text, 80), duration.Seconds())

	return siMessage{
		Text:         result.Text,
		Channel:      msg.Channel,
		Agent:        agent,
		Author:       agent,
		Orchestrator: b.name,
		Timestamp:    time.Now(),
		Meta:         result.Meta,
	}, nil
}

// --- OpenAI Backend ---
// Speaks OpenAI Chat Completions format. Works with OpenClaw, Ollama, vLLM,
// or any OpenAI-compatible API. Full agent runs with tools when backed by OpenClaw.

type OpenAIBackend struct {
	name           string
	url            string            // base URL (e.g., http://localhost:18789)
	token          string            // bearer token
	model          string            // model field (e.g., "openclaw")
	agentHeader    string            // header for agent routing (e.g., "x-openclaw-agent-id")
	agentMap       map[string]string // bus agent name → backend agent ID
	sessionKeyTmpl string            // session key template ({agent} = mapped ID)
	timeout        time.Duration
	client         *http.Client

	// Per-agent conversation history (keyed by "agent:channel").
	mu     sync.Mutex
	convos map[string][]openaiChatMessage
}

const maxConvoMessages = 100 // keep last N messages per conversation

// resolveAgentID maps a bus agent name to the backend's agent ID.
func (b *OpenAIBackend) resolveAgentID(busName string) string {
	if b.agentMap != nil {
		if mapped, ok := b.agentMap[busName]; ok {
			return mapped
		}
	}
	return busName
}

func (b *OpenAIBackend) conversationKey(agent, channel string) string {
	return agent + ":" + channel
}

func (b *OpenAIBackend) appendMessage(key string, msg openaiChatMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.convos == nil {
		b.convos = make(map[string][]openaiChatMessage)
	}
	b.convos[key] = append(b.convos[key], msg)
	// Sliding window: drop oldest messages beyond limit.
	if len(b.convos[key]) > maxConvoMessages {
		b.convos[key] = b.convos[key][len(b.convos[key])-maxConvoMessages:]
	}
}

func (b *OpenAIBackend) getHistory(key string) []openaiChatMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.convos == nil {
		return nil
	}
	msgs := b.convos[key]
	out := make([]openaiChatMessage, len(msgs))
	copy(out, msgs)
	return out
}

func (b *OpenAIBackend) Run(ctx context.Context, agent string, msg siMessage, _ <-chan siMessage, onStream StreamFunc) (siMessage, []spawnRequest) {
	backendAgent := b.resolveAgentID(agent)

	content := msg.Text
	if msg.Author != "" {
		content = fmt.Sprintf("[%s] %s", msg.Author, msg.Text)
	}

	key := b.conversationKey(agent, msg.Channel)
	userMsg := openaiChatMessage{Role: "user", Content: content}
	b.appendMessage(key, userMsg)

	history := b.getHistory(key)

	// Use streaming if callback is provided.
	useStream := onStream != nil

	type streamOpts struct {
		IncludeUsage bool `json:"include_usage"`
	}
	reqBody := struct {
		Model         string              `json:"model"`
		Messages      []openaiChatMessage `json:"messages"`
		Stream        bool                `json:"stream,omitempty"`
		StreamOptions *streamOpts         `json:"stream_options,omitempty"`
	}{
		Model:    b.model,
		Messages: history,
		Stream:   useStream,
	}
	if useStream {
		reqBody.StreamOptions = &streamOpts{IncludeUsage: true}
	}
	data, _ := json.Marshal(reqBody)

	url := strings.TrimRight(b.url, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return errResp(msg, err), nil
	}
	req.Header.Set("Content-Type", "application/json")
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	if b.agentHeader != "" {
		req.Header.Set(b.agentHeader, backendAgent)
	}
	if b.sessionKeyTmpl != "" {
		sessionKey := strings.ReplaceAll(b.sessionKeyTmpl, "{agent}", backendAgent)
		req.Header.Set("x-openclaw-session-key", sessionKey)
	}

	start := time.Now()
	resp, err := b.client.Do(req)
	if err != nil {
		return errResp(msg, err), nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return errResp(msg, fmt.Errorf("http %d: %s", resp.StatusCode,
			truncate(string(body), 200))), nil
	}

	var fullText string
	var model string
	var usage openaiUsage

	if useStream {
		// SSE streaming: read "data: {...}" lines.
		streamID := fmt.Sprintf("s-%d", start.UnixMilli())
		log.Printf("[%s] streaming started for %s (streamID=%s)", b.name, agent, streamID)
		scanner := bufio.NewScanner(resp.Body)
		chunkCount := 0
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue // SSE blank line separator
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				log.Printf("[%s] stream done (%d chunks)", b.name, chunkCount)
				break
			}

			var chunk openaiStreamChunk
			if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
				continue
			}

			if chunk.Model != "" {
				model = chunk.Model
			}

			// Extract usage from final chunk (OpenAI includes it in the last chunk).
			if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
				usage = chunk.Usage
			}

			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta.Content
				if delta != "" {
					chunkCount++
					fullText += delta
					onStream(siMessage{
						Text:         delta,
						Channel:      msg.Channel,
						Agent:        agent,
						Author:       agent,
						Orchestrator: b.name,
						Stream:       "delta",
						StreamID:     streamID,
						Timestamp:    time.Now(),
					})
				}
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("[%s] scanner error: %v", b.name, err)
		}
	} else {
		// Non-streaming: read full response.
		body, _ := io.ReadAll(resp.Body)
		var result openaiChatCompletion
		if err := json.Unmarshal(body, &result); err != nil {
			return errResp(msg, fmt.Errorf("parse response: %w", err)), nil
		}
		if len(result.Choices) > 0 {
			fullText = result.Choices[0].Message.Content
		}
		model = result.Model
		usage = result.Usage
	}

	duration := time.Since(start)

	b.appendMessage(key, openaiChatMessage{Role: "assistant", Content: fullText})

	meta := &messageMeta{
		DurationMs:          duration.Milliseconds(),
		Model:               model,
		InputTokens:         usage.PromptTokens,
		OutputTokens:        usage.CompletionTokens,
		CacheReadTokens:     usage.CacheReadTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
	}

	log.Printf("[%s] → [%s] %s: %s (%.1fs, %d msgs in history)",
		b.name, msg.Channel, agent, truncate(fullText, 80), duration.Seconds(), len(history))

	stream := ""
	streamID := ""
	if useStream {
		stream = "done"
		streamID = fmt.Sprintf("s-%d", start.UnixMilli())
	}

	return siMessage{
		Text:         fullText,
		Channel:      msg.Channel,
		Agent:        agent,
		Author:       agent,
		Orchestrator: b.name,
		Stream:       stream,
		StreamID:     streamID,
		Timestamp:    time.Now(),
		Meta:         meta,
	}, nil
}

// OpenAI API types.
type openaiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
}

type openaiChatCompletion struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      openaiChatMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
	} `json:"choices"`
	Usage openaiUsage `json:"usage"`
}

type openaiStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage openaiUsage `json:"usage"`
}

// --- parse helpers (used by CLIBackend) ---

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
			log.Printf("[cli] failed to parse INBER_SPAWN: %v", err)
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

	// Fallback: parse box-drawing format.
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

// --- shared helpers ---

func formatInput(msg siMessage) string {
	if msg.Author != "" {
		return fmt.Sprintf("[%s] %s", msg.Author, msg.Text)
	}
	return msg.Text
}

func errResp(msg siMessage, err error) siMessage {
	return siMessage{
		Text:      "error: " + err.Error(),
		Channel:   msg.Channel,
		Timestamp: time.Now(),
	}
}

func writeJSON(w io.Writer, text, author string) error {
	msg := struct {
		Text   string `json:"text"`
		Author string `json:"author,omitempty"`
	}{Text: text, Author: author}
	data, _ := json.Marshal(msg)
	_, err := w.Write(append(data, '\n'))
	return err
}

func replaceVars(s, agent string) string {
	return strings.ReplaceAll(s, "{agent}", agent)
}

// stripANSI removes ANSI escape sequences from output.
func stripANSI(b []byte) []byte {
	result := make([]byte, 0, len(b))
	i := 0
	for i < len(b) {
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '[' {
			// Skip CSI sequence: ESC [ ... final byte (0x40-0x7E)
			j := i + 2
			for j < len(b) && (b[j] < 0x40 || b[j] > 0x7E) {
				j++
			}
			if j < len(b) {
				j++ // skip final byte
			}
			i = j
		} else {
			result = append(result, b[i])
			i++
		}
	}
	return result
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + path[1:]
		}
	}
	return path
}
