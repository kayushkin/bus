package messages

import "encoding/json"

// BusEnvelope is the raw message wrapper from the bus WebSocket.
type BusEnvelope struct {
	ID      int64           `json:"id"`
	Topic   string          `json:"topic"`
	Payload json.RawMessage `json:"payload"` // raw JSON
	Source  string          `json:"source"`
}
