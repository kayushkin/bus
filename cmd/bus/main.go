package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kayushkin/bus/hub"
	"github.com/kayushkin/bus/store"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func main() {
	addr := flag.String("addr", envOr("BUS_ADDR", ":8100"), "listen address")
	dbPath := flag.String("db", envOr("BUS_DB", "bus.db"), "SQLite database path")
	token := flag.String("token", envOr("BUS_TOKEN", ""), "auth token (optional)")
	flag.Parse()

	s, err := store.New(*dbPath)
	if err != nil {
		log.Fatalf("failed to open store: %v", err)
	}
	defer s.Close()

	h := hub.New(s)

	// Compaction ticker.
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			n, err := s.Compact()
			if err != nil {
				log.Printf("[compact] error: %v", err)
			} else if n > 0 {
				log.Printf("[compact] removed %d acknowledged messages", n)
			}
		}
	}()

	mux := http.NewServeMux()

	// Auth middleware.
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		if *token == "" {
			return next
		}
		return func(w http.ResponseWriter, r *http.Request) {
			t := r.URL.Query().Get("token")
			if t == "" {
				t = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			}
			if t != *token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	// POST /publish
	mux.HandleFunc("POST /publish", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Topic   string          `json:"topic"`
			Payload json.RawMessage `json:"payload"`
			Source  string          `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Topic == "" {
			http.Error(w, "topic required", http.StatusBadRequest)
			return
		}

		msg, err := h.Publish(req.Topic, req.Payload, req.Source)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(msg)
	}))

	// POST /ack
	mux.HandleFunc("POST /ack", auth(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Consumer  string `json:"consumer"`
			Topic     string `json:"topic"`
			MessageID int64  `json:"message_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if err := s.Ack(req.Consumer, req.Topic, req.MessageID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))

	// GET /history?topic=X&limit=N
	mux.HandleFunc("GET /history", auth(func(w http.ResponseWriter, r *http.Request) {
		topic := r.URL.Query().Get("topic")
		if topic == "" {
			http.Error(w, "topic required", http.StatusBadRequest)
			return
		}

		limit := 50
		if l := r.URL.Query().Get("limit"); l != "" {
			fmt.Sscanf(l, "%d", &limit)
		}

		msgs, err := s.History(topic, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(msgs)
	}))

	// GET /stats
	mux.HandleFunc("GET /stats", auth(func(w http.ResponseWriter, r *http.Request) {
		stats, err := s.Stats()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		stats["subscribers"] = h.SubscriberCount()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	}))

	// WS /subscribe?topics=a,b,c&consumer=X&from=N
	mux.HandleFunc("/subscribe", auth(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[ws] upgrade error: %v", err)
			return
		}
		defer conn.Close()

		consumer := r.URL.Query().Get("consumer")
		if consumer == "" {
			consumer = fmt.Sprintf("ws-%d", time.Now().UnixNano())
		}

		topicStr := r.URL.Query().Get("topics")
		if topicStr == "" {
			topicStr = "*"
		}
		topics := strings.Split(topicStr, ",")

		var fromID int64
		if f := r.URL.Query().Get("from"); f != "" {
			fmt.Sscanf(f, "%d", &fromID)
		}

		sub := h.Subscribe(consumer, topics)
		defer h.Unsubscribe(sub)

		// Catch up on missed messages.
		if fromID > 0 {
			if err := h.CatchUp(sub, fromID); err != nil {
				log.Printf("[ws] catch-up error for %s: %v", consumer, err)
			}
		}

		// Reset read deadline when client responds to our pings.
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(120 * time.Second))
			return nil
		})
		// Also handle client-initiated pings (reset deadline on receipt).
		conn.SetPingHandler(func(appData string) error {
			conn.SetReadDeadline(time.Now().Add(120 * time.Second))
			return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(10*time.Second))
		})

		// Read pump — handle incoming publishes and acks from WS clients.
		go func() {
			for {
				conn.SetReadDeadline(time.Now().Add(120 * time.Second))
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}

				var cmd struct {
					Action  string          `json:"action"` // "publish" or "ack"
					Topic   string          `json:"topic"`
					Payload json.RawMessage `json:"payload"`
					Source  string          `json:"source"`
					ID      int64           `json:"id"` // for ack
				}
				if err := json.Unmarshal(data, &cmd); err != nil {
					continue
				}

				switch cmd.Action {
				case "publish":
					if cmd.Topic != "" {
						h.Publish(cmd.Topic, cmd.Payload, cmd.Source)
					}
				case "ack":
					if cmd.Topic != "" && cmd.ID > 0 {
						s.Ack(consumer, cmd.Topic, cmd.ID)
					}
				}
			}
		}()

		// Keepalive ping.
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}()

		// Write pump — send messages to WS client.
		for msg := range sub.Send {
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}))

	// Health check.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	srv := &http.Server{Addr: *addr, Handler: mux}

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Println("[bus] shutting down...")
		srv.Close()
	}()

	log.Printf("[bus] listening on %s", *addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
