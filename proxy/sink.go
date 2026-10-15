package proxy

import (
	"sync/atomic"

	"github.com/moseleydev/tracelet/core"
)

// Sink hands events from the proxy to whatever consumes them (the CLI log,
// the aggregator, the studio). Emit never blocks: when the consumer falls
// behind, events are dropped and counted rather than slowing down the app.
type Sink struct {
	ch      chan core.Event
	dropped atomic.Uint64
}

// NewSink returns a Sink that buffers up to size events.
func NewSink(size int) *Sink {
	return &Sink{ch: make(chan core.Event, size)}
}

// Emit queues e, or drops it if the buffer is full.
func (s *Sink) Emit(e core.Event) {
	select {
	case s.ch <- e:
	default:
		s.dropped.Add(1)
	}
}

// Events is the stream of captured events. It is closed by Close.
func (s *Sink) Events() <-chan core.Event { return s.ch }

// Dropped reports how many events were discarded because the buffer was full.
func (s *Sink) Dropped() uint64 { return s.dropped.Load() }

// Close ends the event stream. Call it only after every proxy writing to the
// sink has stopped.
func (s *Sink) Close() { close(s.ch) }
