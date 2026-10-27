package onebot

import (
	"encoding/json"
	"log/slog"
	"sync"
)

// Sink is one downstream OneBot consumer: a forward WebSocket connection, a
// reverse WebSocket client, or an HTTP webhook worker.
type Sink interface {
	Key() string
	// Format is "array" or "string".
	Format() string
	// ReportSelf reports whether self-sent messages should be delivered.
	ReportSelf() bool
	SendEvent(ev *Event) error
}

// Hub fans events out to every registered sink.
type Hub struct {
	log *slog.Logger

	mu    sync.RWMutex
	sinks map[string]Sink
	self  func() any
	selfN func() int64
}

// NewHub creates an empty hub.
func NewHub(log *slog.Logger) *Hub {
	return &Hub{
		log:   log,
		sinks: make(map[string]Sink),
		self:  func() any { return 0 },
		selfN: func() int64 { return 0 },
	}
}

// SetSelfFunc installs the provider for the bot's own OneBot id.
func (h *Hub) SetSelfFunc(f func() any) {
	if f == nil {
		return
	}
	h.mu.Lock()
	h.self = f
	h.mu.Unlock()
}

// SetSelfNumFunc installs the provider for the numeric self id used in the
// X-Self-ID header.
func (h *Hub) SetSelfNumFunc(f func() int64) {
	if f == nil {
		return
	}
	h.mu.Lock()
	h.selfN = f
	h.mu.Unlock()
}

// SelfNum returns the numeric self id.
func (h *Hub) SelfNum() int64 {
	h.mu.RLock()
	f := h.selfN
	h.mu.RUnlock()
	return f()
}

// SelfID returns the bot's OneBot user id.
func (h *Hub) SelfID() any {
	h.mu.RLock()
	f := h.self
	h.mu.RUnlock()
	return f()
}

// Add registers a sink, replacing any sink with the same key.
func (h *Hub) Add(s Sink) {
	h.mu.Lock()
	h.sinks[s.Key()] = s
	h.mu.Unlock()
	h.log.Info("onebot sink connected", "sink", s.Key())
}

// Remove unregisters a sink.
func (h *Hub) Remove(key string) {
	h.mu.Lock()
	_, ok := h.sinks[key]
	delete(h.sinks, key)
	h.mu.Unlock()
	if ok {
		h.log.Info("onebot sink disconnected", "sink", key)
	}
}

// Count returns the number of registered sinks.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.sinks)
}

// Broadcast delivers ev to every sink. isSelf marks messages the bot sent
// itself; sinks that opted out of self reporting are skipped.
func (h *Hub) Broadcast(ev *Event, isSelf bool) {
	h.mu.RLock()
	sinks := make([]Sink, 0, len(h.sinks))
	for _, s := range h.sinks {
		sinks = append(sinks, s)
	}
	h.mu.RUnlock()

	for _, s := range sinks {
		if isSelf && !s.ReportSelf() {
			continue
		}
		if err := s.SendEvent(ev); err != nil {
			h.log.Warn("send event failed", "sink", s.Key(), "err", err)
			h.Remove(s.Key())
		}
	}
}

// renderEvent serializes an event using the sink's message format.
func renderEvent(ev *Event, format string) ([]byte, error) {
	if len(ev.Segments) == 0 {
		return json.Marshal(ev)
	}
	clone := *ev
	clone.Message = RenderMessage(ev.Segments, format)
	return json.Marshal(&clone)
}
