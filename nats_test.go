package bus

import (
	"testing"
	"time"
)

func TestPubSub(t *testing.T) {
	c, err := Connect(Options{Name: "test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	done := make(chan string, 1)
	sub, err := c.Subscribe("test.hello", func(subject string, data []byte) {
		done <- string(data)
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	if err := c.Publish("test.hello", map[string]string{"msg": "hi"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got := <-done:
		if got != `{"msg":"hi"}` {
			t.Fatalf("unexpected: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestRequestReply(t *testing.T) {
	c, err := Connect(Options{Name: "test-rr"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	sub, err := c.Reply("test.echo", func(data []byte) (any, error) {
		return map[string]string{"echo": string(data)}, nil
	})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	defer sub.Unsubscribe()

	resp, err := c.Request("test.echo", "ping", 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	t.Logf("response: %s", resp)
}

func TestEnsureStreams(t *testing.T) {
	c, err := Connect(Options{Name: "test-streams"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.Close()

	// Should be idempotent
	if err := c.EnsureStreams(); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}
	if err := c.EnsureStreams(); err != nil {
		t.Fatalf("ensure streams 2nd: %v", err)
	}
}
