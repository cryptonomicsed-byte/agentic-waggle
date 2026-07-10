package main

import (
	"encoding/json"
	"sync"
	"time"
)

// Event is one entry on the substrate's live stream: everything that happens
// (signals, claims, dances, registrations) is observable in real time, both
// by agents (SSE / `wag watch`) and by the Observatory dashboard.
type Event struct {
	Type    string          `json:"type"`
	At      time.Time       `json:"at"`
	Payload json.RawMessage `json:"payload"`
}

// Hub fans events out to any number of subscribers. Slow subscribers are
// dropped rather than allowed to backpressure the swarm.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

func (h *Hub) Subscribe() (ch chan Event, cancel func()) {
	ch = make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) Publish(typ string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ev := Event{Type: typ, At: time.Now(), Payload: raw}
	h.mu.Lock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default: // drop for slow consumers; the stream is advisory
		}
	}
	h.mu.Unlock()
}
