package hub

import (
	"encoding/json"
	"log"
	"sync"

	"github.com/kayushkin/bus/store"
)

// Subscriber receives messages in real-time.
type Subscriber struct {
	ID     string
	Topics map[string]bool
	Send   chan *store.Message
	done   chan struct{}
}

// Hub manages pub/sub with store-backed persistence.
type Hub struct {
	store       *store.Store
	subscribers map[*Subscriber]bool
	mu          sync.RWMutex
}

// New creates a hub backed by the given store.
func New(s *store.Store) *Hub {
	return &Hub{
		store:       s,
		subscribers: make(map[*Subscriber]bool),
	}
}

// Publish persists a message and fans out to live subscribers.
func (h *Hub) Publish(topic string, payload json.RawMessage, source string) (*store.Message, error) {
	msg, err := h.store.Publish(topic, payload, source)
	if err != nil {
		return nil, err
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for sub := range h.subscribers {
		if sub.Topics[topic] || sub.Topics["*"] {
			select {
			case sub.Send <- msg:
			default:
				log.Printf("[hub] slow subscriber %s, dropping message %d", sub.ID, msg.ID)
			}
		}
	}

	return msg, nil
}

// Subscribe registers a subscriber for the given topics.
// Returns the subscriber (call Unsubscribe when done).
func (h *Hub) Subscribe(id string, topics []string) *Subscriber {
	topicMap := make(map[string]bool, len(topics))
	for _, t := range topics {
		topicMap[t] = true
	}

	sub := &Subscriber{
		ID:     id,
		Topics: topicMap,
		Send:   make(chan *store.Message, 256),
		done:   make(chan struct{}),
	}

	h.mu.Lock()
	h.subscribers[sub] = true
	h.mu.Unlock()

	log.Printf("[hub] subscriber %s connected (topics: %v)", id, topics)
	return sub
}

// Unsubscribe removes a subscriber.
func (h *Hub) Unsubscribe(sub *Subscriber) {
	h.mu.Lock()
	delete(h.subscribers, sub)
	h.mu.Unlock()

	close(sub.done)
	log.Printf("[hub] subscriber %s disconnected", sub.ID)
}

// CatchUp sends all messages after fromID for the given topics.
// Used on reconnect to deliver missed messages.
func (h *Hub) CatchUp(sub *Subscriber, fromID int64) error {
	for topic := range sub.Topics {
		if topic == "*" {
			// Wildcard: catch up on all topics? Skip for now.
			continue
		}
		msgs, err := h.store.Since(topic, fromID, 1000)
		if err != nil {
			return err
		}
		for i := range msgs {
			select {
			case sub.Send <- &msgs[i]:
			case <-sub.done:
				return nil
			}
		}
	}
	return nil
}

// Store returns the underlying store (for history/stats queries).
func (h *Hub) Store() *store.Store {
	return h.store
}

// SubscriberCount returns the number of active subscribers.
func (h *Hub) SubscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subscribers)
}
