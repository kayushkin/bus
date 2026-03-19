package bus

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

type Client struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

type Options struct {
	URL  string // default nats://localhost:4222
	Name string // consumer/service name for logging
}

func Connect(opts Options) (*Client, error) {
	url := opts.URL
	if url == "" {
		url = nats.DefaultURL
	}

	var natsOpts []nats.Option
	if opts.Name != "" {
		natsOpts = append(natsOpts, nats.Name(opts.Name))
	}
	natsOpts = append(natsOpts,
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)

	nc, err := nats.Connect(url, natsOpts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}

	return &Client{nc: nc, js: js}, nil
}

func (c *Client) Close() { c.nc.Close() }

// Publish sends JSON-encoded data on a core NATS subject.
func (c *Client) Publish(subject string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return c.nc.Publish(subject, b)
}

// Subscribe to a core NATS subject with raw bytes handler.
func (c *Client) Subscribe(subject string, handler func(subject string, data []byte)) (*nats.Subscription, error) {
	return c.nc.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Subject, msg.Data)
	})
}

// Request sends a request and waits for a reply.
func (c *Client) Request(subject string, data any, timeout time.Duration) ([]byte, error) {
	b, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	msg, err := c.nc.Request(subject, b, timeout)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// Reply subscribes to a subject and sends responses back.
func (c *Client) Reply(subject string, handler func(data []byte) (any, error)) (*nats.Subscription, error) {
	return c.nc.Subscribe(subject, func(msg *nats.Msg) {
		resp, err := handler(msg.Data)
		if err != nil {
			_ = msg.Respond([]byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
			return
		}
		b, err := json.Marshal(resp)
		if err != nil {
			_ = msg.Respond([]byte(fmt.Sprintf(`{"error":%q}`, err.Error())))
			return
		}
		_ = msg.Respond(b)
	})
}

// JetPublish publishes JSON-encoded data via JetStream.
func (c *Client) JetPublish(subject string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = c.js.Publish(subject, b)
	return err
}

// JetSubscribe creates a durable JetStream subscription.
func (c *Client) JetSubscribe(subject string, durable string, handler func(subject string, data []byte)) error {
	_, err := c.js.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Subject, msg.Data)
		_ = msg.Ack()
	}, nats.Durable(durable))
	return err
}

// EnsureStreams creates the standard JetStream streams. Safe to call repeatedly.
func (c *Client) EnsureStreams() error {
	streams := []nats.StreamConfig{
		{
			Name:     "CHAT",
			Subjects: []string{"chat.completed"},
			MaxAge:   30 * 24 * time.Hour,
		},
		{
			Name:     "EVENTS",
			Subjects: []string{"spawn.>", "health.>", "forge.>"},
			MaxAge:   7 * 24 * time.Hour,
		},
		{
			Name:     "LOGS",
			Subjects: []string{"logs.>"},
			MaxAge:   90 * 24 * time.Hour,
		},
	}

	for _, cfg := range streams {
		_, err := c.js.AddStream(&cfg)
		if err != nil && err != nats.ErrStreamNameAlreadyInUse {
			// Also check for the API error
			if apiErr, ok := err.(*nats.APIError); ok && apiErr.ErrorCode == 10058 {
				continue
			}
			return fmt.Errorf("create stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}
