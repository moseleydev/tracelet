package proxy

import (
	"sync"
	"testing"
	"time"

	"github.com/moseleydev/tracelet/core"
)

func TestSinkDeliversInBatches(t *testing.T) {
	s := NewSink(100)
	for i := 0; i < 3; i++ {
		s.Emit(core.Event{Rows: int64(i)})
	}
	batch, ok := s.Batch(nil)
	if !ok || len(batch) != 3 || batch[2].Rows != 2 {
		t.Fatalf("batch = %+v, ok = %v", batch, ok)
	}
	// Nothing pending: Batch waits briefly, then returns empty but open.
	start := time.Now()
	if batch, ok := s.Batch(batch); len(batch) != 0 || !ok || time.Since(start) < batchWait/2 {
		t.Fatalf("empty batch = %+v, ok = %v after %v", batch, ok, time.Since(start))
	}
}

func TestSinkDropsWhenFullInsteadOfBlocking(t *testing.T) {
	s := NewSink(2)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			s.Emit(core.Event{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked on a full sink")
	}
	if s.Dropped() != 3 {
		t.Fatalf("dropped = %d, want 3", s.Dropped())
	}
}

func TestSinkCloseDeliversRemainingEvents(t *testing.T) {
	s := NewSink(10)
	s.Emit(core.Event{Rows: 7})
	s.Close()
	s.Emit(core.Event{}) // after close: dropped
	batch, ok := s.Batch(nil)
	if !ok || len(batch) != 1 || batch[0].Rows != 7 {
		t.Fatalf("final batch = %+v, ok = %v", batch, ok)
	}
	if _, ok := s.Batch(nil); ok {
		t.Fatal("a closed, drained sink should report !ok")
	}
}

func TestSinkConcurrentEmitters(t *testing.T) {
	s := NewSink(1 << 20)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10_000; i++ {
				s.Emit(core.Event{})
			}
		}()
	}
	total := 0
	stop := make(chan struct{})
	go func() { wg.Wait(); s.Close(); close(stop) }()
	var spare []core.Event
	for {
		batch, ok := s.Batch(spare)
		total += len(batch)
		spare = batch
		if !ok {
			break
		}
	}
	<-stop
	if total != 80_000 || s.Dropped() != 0 {
		t.Fatalf("received %d events, dropped %d; want 80000, 0", total, s.Dropped())
	}
}

func BenchmarkEmit(b *testing.B) {
	s := NewSink(1 << 30)
	e := core.Event{Query: "SELECT 1"}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			s.Emit(e)
		}
	})
}
