package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kayushkin/bus/store"
)

// Client connects to the bus over HTTP and WebSocket.
type Client struct {
	baseURL    string
	wsURL      string
	token      string
	consumer   string
	httpClient *http.Client
}

// New creates a bus client.
// baseURL is the HTTP base (e.g., "http://localhost:8100").
func New(baseURL, token, consumer string) *Client {
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)

	return &Client{
		baseURL:    baseURL,
		wsURL:      wsURL,
		token:      token,
		consumer:   consumer,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Publish sends a message to a topic.
func (c *Client) Publish(topic string, payload interface{}, source string) (*store.Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	body := map[string]interface{}{
		"topic":   topic,
		"payload": json.RawMessage(data),
		"source":  source,
	}

	resp, err := c.post("/publish", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var msg store.Message
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// Ack acknowledges a message.
func (c *Client) Ack(topic string, messageID int64) error {
	body := map[string]interface{}{
		"consumer":   c.consumer,
		"topic":      topic,
		"message_id": messageID,
	}
	resp, err := c.post("/ack", body)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// History fetches recent messages on a topic.
func (c *Client) History(topic string, limit int) ([]store.Message, error) {
	url := fmt.Sprintf("%s/history?topic=%s&limit=%d", c.baseURL, topic, limit)
	if c.token != "" {
		url += "&token=" + c.token
	}

	resp, err := c.httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}

	var msgs []store.Message
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

// Handler is called for each received message.
type Handler func(msg *store.Message)

// Subscription manages a WebSocket subscription with auto-reconnect.
type Subscription struct {
	client  *Client
	topics  []string
	handler Handler
	fromID  int64
	cancel  chan struct{}
	mu      sync.Mutex
}

// Subscribe connects via WebSocket and delivers messages to the handler.
// Automatically reconnects and catches up on missed messages.
func (c *Client) Subscribe(topics []string, fromID int64, handler Handler) *Subscription {
	sub := &Subscription{
		client:  c,
		topics:  topics,
		handler: handler,
		fromID:  fromID,
		cancel:  make(chan struct{}),
	}
	go sub.run()
	return sub
}

// Close stops the subscription.
func (s *Subscription) Close() {
	close(s.cancel)
}

func (s *Subscription) run() {
	for {
		select {
		case <-s.cancel:
			return
		default:
		}

		if err := s.connect(); err != nil {
			log.Printf("[bus-client] connection error: %v, reconnecting in 3s...", err)
			select {
			case <-s.cancel:
				return
			case <-time.After(3 * time.Second):
			}
		}
	}
}

func (s *Subscription) connect() error {
	url := fmt.Sprintf("%s/subscribe?consumer=%s&topics=%s&from=%d",
		s.client.wsURL,
		s.client.consumer,
		strings.Join(s.topics, ","),
		s.fromID,
	)
	if s.client.token != "" {
		url += "&token=" + s.client.token
	}

	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	for {
		select {
		case <-s.cancel:
			return nil
		default:
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg store.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		s.handler(&msg)

		// Track latest ID for reconnect catch-up.
		s.mu.Lock()
		if msg.ID > s.fromID {
			s.fromID = msg.ID
		}
		s.mu.Unlock()
	}
}

func (c *Client) post(path string, body interface{}) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := c.baseURL + path
	if c.token != "" {
		url += "?token=" + c.token
	}

	resp, err := c.httpClient.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}

	return resp, nil
}
