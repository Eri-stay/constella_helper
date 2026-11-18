package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"constella/internal/platform/events"
)

// Hub fans broker events out to the browsers connected to /api/stream.
type Hub struct {
	log *slog.Logger

	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewHub returns an empty hub.
func NewHub(log *slog.Logger) *Hub {
	return &Hub{log: log, subs: map[chan []byte]struct{}{}}
}

// Handle is the broker consumer: it frames the event and sends it to every
// subscriber. A subscriber that cannot keep up misses events instead of
// blocking the broker.
func (h *Hub) Handle(_ context.Context, env events.Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal event %s: %w", env.ID, err)
	}
	msg := []byte("event: " + env.Type + "\ndata: " + string(body) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
			h.log.Warn("sse subscriber too slow; event dropped", "type", env.Type)
		}
	}
	return nil
}

func (h *Hub) subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// ServeHTTP streams events as Server-Sent Events until the client leaves.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := h.subscribe()
	defer h.unsubscribe(ch)

	// The first line makes the browser's EventSource fire "open" at once.
	_, _ = fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if _, err := w.Write(msg); err != nil {
				return
			}
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
		}
		fl.Flush()
	}
}
