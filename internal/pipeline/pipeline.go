// Package pipeline applies the delivery rules that sit between the WeChat
// backend and the OneBot transports: deduplication, self-message filtering and
// group mention filtering.
package pipeline

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"wxcat/internal/backend"
	"wxcat/internal/config"
)

const dedupeTTL = 10 * time.Minute

// Filter decides whether a raw backend event should be published.
type Filter struct {
	reportSelf bool
	groupMode  string
	keywords   []string

	mu       sync.Mutex
	seen     map[string]time.Time
	selfID   string
	selfName string
}

// New creates a filter from configuration.
func New(cfg *config.Config) *Filter {
	return &Filter{
		reportSelf: cfg.ReportSelfMessage,
		groupMode:  cfg.GroupMode,
		keywords:   cfg.AtKeywords,
		seen:       make(map[string]time.Time),
	}
}

// SetSelf records the bot identity used for mention detection.
func (f *Filter) SetSelf(id, name string) {
	f.mu.Lock()
	f.selfID = id
	f.selfName = name
	f.mu.Unlock()
}

// Allow reports whether ev should be published.
func (f *Filter) Allow(ev backend.RawEvent) bool {
	if ev.IsSelf && !f.reportSelf {
		return false
	}
	if f.duplicate(ev) {
		return false
	}
	if ev.Type == backend.EventNotice {
		return true
	}
	if ev.Target.Kind != backend.KindGroup {
		return true
	}
	if f.groupMode == "all" {
		return true
	}
	return f.mentioned(ev)
}

func (f *Filter) mentioned(ev backend.RawEvent) bool {
	f.mu.Lock()
	selfID, selfName := f.selfID, f.selfName
	keywords := append([]string(nil), f.keywords...)
	f.mu.Unlock()

	for _, at := range ev.AtList {
		if selfID != "" && at == selfID {
			return true
		}
	}
	if selfName != "" && strings.Contains(ev.Text, "@"+selfName) {
		return true
	}
	for _, kw := range keywords {
		if kw != "" && strings.Contains(ev.Text, kw) {
			return true
		}
	}
	return false
}

func (f *Filter) duplicate(ev backend.RawEvent) bool {
	if ev.MsgID == 0 {
		return false
	}
	key := fmt.Sprintf("%s:%d", ev.Target.ID, ev.MsgID)
	now := time.Now()

	f.mu.Lock()
	defer f.mu.Unlock()

	if t, ok := f.seen[key]; ok && now.Sub(t) < dedupeTTL {
		return true
	}
	f.seen[key] = now

	if len(f.seen) > 4096 {
		for k, t := range f.seen {
			if now.Sub(t) >= dedupeTTL {
				delete(f.seen, k)
			}
		}
	}
	return false
}
