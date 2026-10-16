package proxy

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/moseleydev/tracelet/core"
)

// Sink hands events from the proxy to whatever consumes them (the CLI log,
// the aggregator, the studio).
//
// It is built for the proxy's hot path: Emit only appends to a buffer under
// a short lock. It never blocks, never allocates beyond the buffer, and
// never wakes another goroutine. (A channel send would wake the consumer
// once per query, and at tens of thousands of queries a second those
// wake-ups cost more than everything else Tracelet does.) The consumer
// collects events in batches with Batch instead. When the buffer is full,
// events are dropped and counted rather than slowing down the application.
type Sink struct {
	mu      sync.Mutex
	buf     []core.Event
	size    int
	closed  bool
	dropped atomic.Uint64
}

// batchWait is how long Batch sleeps when there is nothing to collect.
// It bounds how stale the live view can be, at a cost of at most a few
// hundred wake-ups a second regardless of traffic.
const batchWait = 5 * time.Millisecond

// NewSink returns a Sink that buffers up to size events between batches.
func NewSink(size int) *Sink {
	return &Sink{size: size, buf: make([]core.Event, 0, min(size, 1024))}
}

// Emit queues e, or drops it if the buffer is full.
func (s *Sink) Emit(e core.Event) {
	s.mu.Lock()
	if len(s.buf) >= s.size || s.closed {
		s.mu.Unlock()
		s.dropped.Add(1)
		return
	}
	s.buf = append(s.buf, e)
	s.mu.Unlock()
}

// Batch swaps out everything emitted since the last call, reusing spare as
// the next buffer. If nothing is pending it waits briefly first. ok is false
// once the sink is closed and fully drained.
func (s *Sink) Batch(spare []core.Event) (batch []core.Event, ok bool) {
	s.mu.Lock()
	if len(s.buf) == 0 && !s.closed {
		s.mu.Unlock()
		time.Sleep(batchWait)
		s.mu.Lock()
	}
	batch, s.buf = s.buf, spare[:0]
	ok = len(batch) > 0 || !s.closed
	s.mu.Unlock()
	return batch, ok
}

// Dropped reports how many events were discarded because the buffer was full.
func (s *Sink) Dropped() uint64 { return s.dropped.Load() }

// Close ends the event stream. Events already emitted are still delivered.
// Call it only after every proxy writing to the sink has stopped.
func (s *Sink) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}
