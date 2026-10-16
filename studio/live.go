package studio

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/moseleydev/tracelet/core"
)

// LiveEvent is one query as sent to the browser. It carries the fingerprint,
// never the raw query, so literal values never reach the page.
type LiveEvent struct {
	Hash     string   `json:"h"`
	Query    string   `json:"q"`
	Tables   []string `json:"t"`
	Duration int64    `json:"d"` // microseconds
	Rows     int64    `json:"r"`
	Error    bool     `json:"e,omitempty"`
	At       int64    `json:"at"`           // unix milliseconds, completion time
	Route    string   `json:"rt,omitempty"` // request route, from sqlcommenter
}

// liveBatch is what one Server-Sent Event carries.
type liveBatch struct {
	Events []LiveEvent `json:"events"`
	// Total is how many queries completed in this interval. It can exceed
	// len(Events) when traffic is heavy and events were sampled.
	Total int `json:"total"`
}

const (
	batchInterval  = 100 * time.Millisecond
	maxBatchEvents = 400 // per interval; enough for smooth animation
	subscriberBuf  = 32
)

// Hub fans live events out to every connected browser. Publishing never
// blocks: slow browsers miss batches rather than slowing Tracelet down.
type Hub struct {
	mu      sync.Mutex
	pending []LiveEvent
	total   int
	subs    map[chan []byte]struct{}
}

// NewHub returns a Hub. Call Run to start broadcasting.
func NewHub() *Hub {
	return &Hub{subs: map[chan []byte]struct{}{}}
}

// Publish queues one completed query for the next batch.
func (h *Hub) Publish(e core.Event, fp core.Fingerprint) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.total++
	if len(h.subs) == 0 || len(h.pending) >= maxBatchEvents {
		return
	}
	h.pending = append(h.pending, LiveEvent{
		Hash:     fp.Hash,
		Query:    fp.Query,
		Tables:   fp.Tables,
		Duration: e.Duration.Microseconds(),
		Rows:     e.Rows,
		Error:    e.Error != "",
		At:       e.StartedAt.Add(e.Duration).UnixMilli(),
		Route:    e.Route,
	})
}

// Run broadcasts a batch every interval until stop is closed.
func (h *Hub) Run(stop <-chan struct{}) {
	t := time.NewTicker(batchInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			h.mu.Lock()
			for ch := range h.subs {
				close(ch)
				delete(h.subs, ch)
			}
			h.mu.Unlock()
			return
		case <-t.C:
			h.flush()
		}
	}
}

func (h *Hub) flush() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) == 0 {
		h.pending, h.total = h.pending[:0], 0
		return
	}
	events := h.pending
	if events == nil {
		events = []LiveEvent{}
	}
	msg, _ := json.Marshal(liveBatch{Events: events, Total: h.total})
	h.pending, h.total = nil, 0
	for ch := range h.subs {
		select {
		case ch <- msg:
		default: // browser is behind; it skips this batch
		}
	}
}

func (h *Hub) subscribe() chan []byte {
	ch := make(chan []byte, subscriberBuf)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}
