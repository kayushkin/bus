// bus-agent subscribes to the bus for inbound messages,
// runs inber locally, and publishes responses back.
// Replaces tunnel-client.
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
	Text    string `json:"text"`
	Author  string `json:"author,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Channel string `json:"channel,omitempty"`
}

func main() {
	busURL := flag.String("bus", envOr("BUS_URL", "https://kayushkin.com/bus"), "bus URL")
	token := flag.String("token", envOr("BUS_TOKEN", ""), "bus auth token")
	consumer := flag.String("consumer", envOr("BUS_CONSUMER", "bus-agent-wsl"), "consumer ID")
	inberBin := flag.String("inber", envOr("INBER_BIN", os.ExpandEnv("$HOME/bin/inber")), "inber binary path")
	inberDir := flag.String("dir", envOr("INBER_DIR", os.ExpandEnv("$HOME/life/repos/inber")), "inber working directory")
	topics := flag.String("topics", envOr("BUS_TOPICS", "inbound.*,inbound.default"), "topics to subscribe to")
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

	agent := &Agent{
		busURL:   *busURL,
		token:    *token,
		consumer: *consumer,
		inberBin: *inberBin,
		inberDir: *inberDir,
		topics:   strings.Split(*topics, ","),
		http:     &http.Client{Timeout: 10 * time.Second},
	}

	agent.Run(ctx)
}

type Agent struct {
	busURL   string
	token    string
	consumer string
	inberBin string
	inberDir string
	topics   []string
	http     *http.Client
	mu       sync.Mutex
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

	url := fmt.Sprintf("%s/subscribe?consumer=%s&topics=%s&token=%s",
		wsURL, a.consumer, strings.Join(a.topics, ","), a.token)

	log.Printf("[bus-agent] connecting to %s...", a.busURL)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	log.Printf("[bus-agent] subscribed to %v", a.topics)

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

		log.Printf("[bus-agent] received: %s", truncate(siMsg.Text, 80))

		// Process async.
		go func(m siMessage, busID int64, topic string) {
			resp := a.processWithInber(ctx, m)
			a.publish(resp)
			a.ack(topic, busID)
		}(siMsg, msg.ID, msg.Topic)
	}
}

func (a *Agent) processWithInber(ctx context.Context, msg siMessage) siMessage {
	// Build context prefix.
	contextPrefix := ""
	if msg.Channel != "" {
		contextPrefix = "[from " + msg.Channel
		if msg.Author != "" {
			contextPrefix += " by " + msg.Author
		}
		contextPrefix += "] "
	}

	agent := msg.Agent
	args := []string{"run"}
	if agent != "" {
		args = append(args, "--agent", agent)
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
		return siMessage{Text: "error: " + err.Error(), Channel: msg.Channel}
	}

	input := contextPrefix + msg.Text
	stdin.Write([]byte(input))
	stdin.Close()

	output, _ := io.ReadAll(stdout)
	errData, _ := io.ReadAll(stderr)
	cmd.Wait()

	response := siMessage{
		Text:    string(output),
		Channel: msg.Channel,
		Author:  agent,
		Agent:   agent,
	}

	if len(output) == 0 && len(errData) > 0 {
		response.Text = string(errData)
	}

	log.Printf("[bus-agent] response: %s", truncate(response.Text, 80))
	return response
}

func (a *Agent) publish(msg siMessage) {
	topic := "outbound." + msg.Agent
	if msg.Agent == "" {
		topic = "outbound.default"
	}

	payload, _ := json.Marshal(msg)
	body := map[string]interface{}{
		"topic":   topic,
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
