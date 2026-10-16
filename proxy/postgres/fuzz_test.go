package postgres

import (
	"sync/atomic"
	"testing"

	"github.com/moseleydev/tracelet/proxy"
)

// FuzzTracker feeds arbitrary protocol messages, in arbitrary order, to the
// tracker. It must never panic: the proxy relies on it to read every
// message an application or database might send.
func FuzzTracker(f *testing.F) {
	f.Add([]byte("FQSELECT 1\x00BZ\x00"))
	f.Add([]byte("FPs1\x00SELECT $1\x00\x00\x00FBp\x00s1\x00\x00\x01\x00\x01\x00\x01\x00\x00\x00\x02hi\x00\x00FEp\x00\x00\x00\x00\x00FS"))
	f.Add([]byte("BCSELECT 5\x00BESERROR\x00C42P01\x00Mnope\x00\x00BZI"))
	f.Add([]byte("FC\x00BI\x00Bs\x00B1B2FB\xff\xff"))
	f.Fuzz(func(t *testing.T, data []byte) {
		recording := &atomic.Bool{}
		recording.Store(true) // exercise the replay capture too
		srv := &Server{cfg: Config{Sink: proxy.NewSink(64), Logf: func(string, ...any) {}, CaptureParams: true, Recording: recording}}
		tr := newTracker(srv, 1)
		// Each message: direction byte (F/B), type byte, then a body that
		// runs to the next direction marker or a length taken from data.
		for len(data) >= 2 {
			dir, typ := data[0], data[1]
			data = data[2:]
			n := 0
			for n < len(data) && data[n] != 'F' && data[n] != 'B' {
				n++
			}
			body := data[:n]
			data = data[n:]
			if dir == 'F' {
				tr.frontend(typ, body, true)
			} else {
				tr.backend(typ, body, true)
			}
		}
	})
}

func FuzzBindParams(f *testing.F) {
	f.Add([]byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 2, 'h', 'i'})
	f.Add([]byte{0, 2, 0, 0, 0, 1, 0, 2, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1, 7})
	f.Fuzz(func(t *testing.T, data []byte) {
		bindParams(data)
	})
}
