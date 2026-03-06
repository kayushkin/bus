// bus-agent subscribes to the bus for inbound messages,
// calls inber locally, and publishes responses back.
//
// Si publishes all adapter messages to "inbound" with channel metadata.
// Bus-agent determines the right agent from channel config, calls inber,
// publishes the response to "outbound" with the original channel info.
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

type busMessage struct {
	ID      int64           `json:"id"`
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload"`
	Source  string          `json:"source"`
}

type siMessage struct {
	ID        string    `json:"id,omitempty"`
	Text      string    `json:"text"`
	Author    string    `json:"author,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Channel   string    `json:"channel,omitempty"`
	ReplyTo   string    `json:"reply_to,omitempty"`
	MediaURL  string    `json:"media_url,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// channelRoute maps a channel pattern to an agent name.
type channelRoute struct {
	Prefix string `json:"prefix"` // e.g. "discord:", "websocket"
	Agent  string `json:"agent"`  // e.g. "claxon", "bran"
}

func main() {
	busURL := flag.String("bus", envOr("BUS_URL", "http://localhost:8100"), "bus URL")
	token := flag.String("token", envOr("BUS_TOKEN", ""), "bus auth token")
	consumer := flag.String("consumer", envOr("BUS_CONSUMER", "bus-agent-wsl"), "consumer ID")
	inberBin := flag.String("inber", envOr("INBER_BIN", os.ExpandEnv("$HOME/bin/inber")), "inber binary path")
	inberDir := flag.String("dir", envOr("INBER_DIR", os.ExpandEnv("$HOME/life/repos/inber")), "inber working directory")
	defaultAgent := flag.String("agent", envOr("BUS_DEFAULT_AGENT", "claxon"), "default agent")
	routesFile := flag.String("routes", envOr("BUS_ROUTES", ""), "channel routes JSON file (optional)")
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

	// Load channel → agent routes.
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

	agent := &Agent{
		busURL:       *busURL,
		token:        *token,
		consumer:     *consumer,
		inberBin:     *inberBin,
		inberDir:     *inberDir,
		defaultAgent: *defaultAgent,
		routes:       routes,
		http:         &http.Client{Timeout: 10 * time.Second},
	}

	agent.Run(ctx)
}

type Agent struct {
	busURL       string
	token        string
	consumer     string
	inberBin     string
	inberDir     string
	defaultAgent string
	routes       []channelRoute
	http         *http.Client
	mu           sync.Mutex // serialize inber calls per agent
}

// resolveAgent maps a channel to an agent name using configured routes.
func (a *Agent) resolveAgent(channel string) string {
	for _, r := range a.routes {
		if strings.HasPrefix(channel, r.Prefix) || channel == r.Prefix {
			return r.Agent
		}
	}
	return a.defaultAgent
}

func (a *Agent) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := a.subscribe(ctx); err != nil {
			log.Printf("[bus-agent] error: %v, reconnecting in 3s...", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (a *Agent) subscribe(ctx context.Context) error {
	wsURL := strings.Replace(a.busURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)

	url := fmt.Sprintf("%s/subscribe?consumer=%s&topics=inbound&token=%s",
		wsURL, a.consumer, a.token)

	log.Printf("[bus-agent] connecting to %s...", a.busURL)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	log.Printf("[bus-agent] subscribed to inbound")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
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

		// Process synchronously to avoid concurrent inber sessions.
		// TODO: per-agent goroutines with queues for concurrency.
		resp := a.processWithInber(ctx, siMsg)
		a.publish(resp)
		a.ack(msg.Topic, msg.ID)
	}
}

func (a *Agent) processWithInber(ctx context.Context, msg siMessage) siMessage {
	agent := a.resolveAgent(msg.Channel)

	args := []string{"run", "-a", agent}

	// Format input with metadata so the agent knows who's talking.
	input := msg.Text
	if msg.Author != "" {
		input = fmt.Sprintf("[%s] %s", msg.Author, msg.Text)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, a.inberBin, args...)
	cmd.Dir = a.inberDir

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return siMessage{Text: "error: " + err.Error(), Channel: msg.Channel}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return siMessage{Text: "error: " + err.Error(), Channel: msg.Channel}
	}

	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		return siMessage{Text: "error starting inber: " + err.Error(), Channel: msg.Channel}
	}

	stdin.Write([]byte(input))
	stdin.Close()

	output, _ := io.ReadAll(stdout)
	errData, _ := io.ReadAll(stderr)
	cmd.Wait()

	text := strings.TrimSpace(string(output))
	if text == "" && len(errData) > 0 {
		text = strings.TrimSpace(string(errData))
	}

	log.Printf("[bus-agent] → [%s] %s: %s", msg.Channel, agent, truncate(text, 80))

	return siMessage{
		Text:      text,
		Channel:   msg.Channel,
		Author:    agent,
		Timestamp: time.Now(),
	}
}

func (a *Agent) publish(msg siMessage) {
	payload, _ := json.Marshal(msg)
	body := map[string]interface{}{
		"topic":   "outbound",
		"payload": json.RawMessage(payload),
		"source":  "bus-agent",
	}
	data, _ := json.Marshal(body)

	url := a.busURL + "/publish?token=" + a.token
	resp, err := a.http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("[bus-agent] publish error: %v", err)
		return
	}
	resp.Body.Close()
}

func (a *Agent) ack(topic string, id int64) {
	body := map[string]interface{}{
		"consumer":   a.consumer,
		"topic":      topic,
		"message_id": id,
	}
	data, _ := json.Marshal(body)
	url := a.busURL + "/ack?token=" + a.token
	resp, err := a.http.Post(url, "application/json", bytes.NewReader(data))
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
