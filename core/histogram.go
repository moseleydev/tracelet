package core

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// Histogram records a latency distribution in fixed log-scale buckets, each
// 10% wider than the last, from 1µs up to several hours. Percentiles are
// accurate to about ±5%, histograms from different time windows merge by
// adding counts, and an encoded histogram is usually a few dozen bytes.
type Histogram struct {
	Counts [histBuckets]uint32
	N      uint64
	Min    time.Duration
	Max    time.Duration
}

const (
	histBuckets = 256
	histGrowth  = 1.1
	histBase    = time.Microsecond
)

var logGrowth = math.Log(histGrowth)

func bucketOf(d time.Duration) int {
	if d < histBase {
		return 0
	}
	i := int(math.Log(float64(d)/float64(histBase))/logGrowth) + 1
	if i >= histBuckets {
		return histBuckets - 1
	}
	return i
}

// bucketMid is the geometric midpoint of bucket i.
func bucketMid(i int) time.Duration {
	if i == 0 {
		return histBase / 2
	}
	lo := float64(histBase) * math.Pow(histGrowth, float64(i-1))
	return time.Duration(lo * math.Sqrt(histGrowth))
}

// Record adds one observation.
func (h *Histogram) Record(d time.Duration) {
	h.Counts[bucketOf(d)]++
	if h.N == 0 || d < h.Min {
		h.Min = d
	}
	if d > h.Max {
		h.Max = d
	}
	h.N++
}

// Merge adds every observation in o to h.
func (h *Histogram) Merge(o *Histogram) {
	if o == nil || o.N == 0 {
		return
	}
	for i, c := range o.Counts {
		h.Counts[i] += c
	}
	if h.N == 0 || o.Min < h.Min {
		h.Min = o.Min
	}
	if o.Max > h.Max {
		h.Max = o.Max
	}
	h.N += o.N
}

// Percentile returns the latency below which p (0–100) percent of
// observations fall.
func (h *Histogram) Percentile(p float64) time.Duration {
	if h.N == 0 {
		return 0
	}
	rank := uint64(math.Ceil(p / 100 * float64(h.N)))
	if rank < 1 {
		rank = 1
	}
	var seen uint64
	for i, c := range h.Counts {
		seen += uint64(c)
		if seen >= rank {
			if i == histBuckets-1 {
				return h.Max // overflow bucket has no meaningful midpoint
			}
			return min(max(bucketMid(i), h.Min), h.Max)
		}
	}
	return h.Max
}

// MarshalBinary encodes h sparsely: min, max, then (gap, count) pairs for
// non-empty buckets, all as varints.
func (h *Histogram) MarshalBinary() ([]byte, error) {
	b := binary.AppendUvarint(nil, uint64(h.Min))
	b = binary.AppendUvarint(b, uint64(h.Max))
	last := -1
	for i, c := range h.Counts {
		if c == 0 {
			continue
		}
		b = binary.AppendUvarint(b, uint64(i-last))
		b = binary.AppendUvarint(b, uint64(c))
		last = i
	}
	return b, nil
}

// UnmarshalBinary decodes the output of MarshalBinary.
func (h *Histogram) UnmarshalBinary(b []byte) error {
	*h = Histogram{}
	read := func() (uint64, bool) {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			return 0, false
		}
		b = b[n:]
		return v, true
	}
	minV, ok1 := read()
	maxV, ok2 := read()
	if !ok1 || !ok2 {
		return errors.New("histogram: truncated header")
	}
	h.Min, h.Max = time.Duration(minV), time.Duration(maxV)
	i := -1
	for len(b) > 0 {
		gap, ok1 := read()
		c, ok2 := read()
		i += int(gap)
		if !ok1 || !ok2 || gap == 0 || i >= histBuckets {
			return errors.New("histogram: corrupt bucket data")
		}
		h.Counts[i] = uint32(c)
		h.N += c
	}
	return nil
}
