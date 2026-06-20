// Package ws provides an in-memory pub/sub hub for streaming run events and
// logs over WebSocket connections. It is per-process only; NATS replaces this
// in Phase 6 for multi-instance deployments.
package ws

import (
	"sync"
)

// Hub is a per-process in-memory pub/sub dispatcher. Subscribe returns a
// channel that receives byte-serialized messages published to the topic key.
// Call the returned unsubscribe function to clean up.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[string][]chan []byte
}

// NewHub returns a ready Hub.
func NewHub() *Hub {
	return &Hub{
		subscribers: make(map[string][]chan []byte),
	}
}

// Subscribe registers a channel for the given topic (typically a run ID) and
// returns it along with an unsubscribe function. The channel has a buffer of
// 64 messages so slow consumers don't block publishers.
func (h *Hub) Subscribe(topic string) (<-chan []byte, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan []byte, 64)
	h.subscribers[topic] = append(h.subscribers[topic], ch)

	unsub := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		subs := h.subscribers[topic]
		for i, sub := range subs {
			if sub == ch {
				h.subscribers[topic] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
		// Clean up empty topic entries.
		if len(h.subscribers[topic]) == 0 {
			delete(h.subscribers, topic)
		}
	}
	return ch, unsub
}

// Publish sends msg to every subscriber of topic. Slow or blocked subscribers
// are dropped (non-blocking send).
func (h *Hub) Publish(topic string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, ch := range h.subscribers[topic] {
		select {
		case ch <- msg:
		default:
			// Subscriber too slow — drop the message.
		}
	}
}

// PublishRunEvent satisfies the run.EventPublisher interface. It is a thin
// wrapper around Publish.
func (h *Hub) PublishRunEvent(runID string, data []byte) {
	h.Publish(runID, data)
}
