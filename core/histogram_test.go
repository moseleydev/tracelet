package core

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"
)

func TestHistogramPercentilesWithinFivePercent(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	var h Histogram
	var exact []time.Duration
	for i := 0; i < 50_000; i++ {
		// Log-normal latencies centered near 2ms with a long tail.
		d := time.Duration(math.Exp(r.NormFloat64()*1.2) * float64(2*time.Millisecond))
		h.Record(d)
		exact = append(exact, d)
	}
	sort.Slice(exact, func(i, j int) bool { return exact[i] < exact[j] })

	for _, p := range []float64{50, 90, 95, 99, 99.9} {
		want := exact[int(math.Ceil(p/100*float64(len(exact))))-1]
		got := h.Percentile(p)
		if err := math.Abs(float64(got-want)) / float64(want); err > 0.05 {
			t.Errorf("p%v = %v, exact %v (error %.1f%%)", p, got, want, err*100)
		}
	}
	if h.Max != exact[len(exact)-1] || h.Min != exact[0] {
		t.Errorf("min/max = %v/%v, want %v/%v", h.Min, h.Max, exact[0], exact[len(exact)-1])
	}
}

func TestHistogramEdgeValues(t *testing.T) {
	var h Histogram
	if h.Percentile(99) != 0 {
		t.Fatal("empty histogram should report 0")
	}
	h.Record(0)
	h.Record(500 * time.Nanosecond)
	h.Record(48 * time.Hour) // beyond the last bucket
	if h.Max != 48*time.Hour || h.Percentile(100) != 48*time.Hour {
		t.Fatalf("max = %v, p100 = %v", h.Max, h.Percentile(100))
	}
	if p1 := h.Percentile(1); p1 >= time.Microsecond {
		t.Fatalf("p1 = %v, want sub-microsecond", p1)
	}
}

func TestHistogramMergeAndEncode(t *testing.T) {
	var a, b Histogram
	for i := 1; i <= 100; i++ {
		a.Record(time.Duration(i) * time.Millisecond)
		b.Record(time.Duration(i) * time.Second)
	}
	a.Merge(&b)
	if a.N != 200 || a.Min != time.Millisecond || a.Max != 100*time.Second {
		t.Fatalf("merged N/min/max = %d/%v/%v", a.N, a.Min, a.Max)
	}

	enc, err := a.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var dec Histogram
	if err := dec.UnmarshalBinary(enc); err != nil {
		t.Fatal(err)
	}
	if dec != a {
		t.Fatal("decoded histogram differs from original")
	}
	if len(enc) > 200 {
		t.Errorf("encoding is %d bytes, expected compact", len(enc))
	}
	if err := dec.UnmarshalBinary(enc[:len(enc)-1]); err == nil {
		t.Error("truncated input should fail to decode")
	}
}

func TestSummarize(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mk := func(bucket time.Time, ds ...time.Duration) Rollup {
		r := Rollup{Hash: "h", Bucket: bucket}
		for _, d := range ds {
			r.Count++
			r.Total += d
			r.Rows += 2
			r.Hist.Record(d)
		}
		return r
	}
	later := mk(t0.Add(10*time.Second), 30*time.Millisecond)
	later.Errors, later.LastError = 1, "boom"

	s := Summarize(Fingerprint{Hash: "h"}, []Rollup{later, mk(t0, 10*time.Millisecond, 20*time.Millisecond)}, 10*time.Second)
	if s.Count != 3 || s.Errors != 1 || s.Total != 60*time.Millisecond || s.Mean != 20*time.Millisecond {
		t.Fatalf("count/errors/total/mean = %d/%d/%v/%v", s.Count, s.Errors, s.Total, s.Mean)
	}
	if s.MeanRows != 2 || s.Max != 30*time.Millisecond || s.LastError != "boom" {
		t.Fatalf("rows/max/err = %v/%v/%q", s.MeanRows, s.Max, s.LastError)
	}
	if !s.FirstSeen.Equal(t0) || !s.LastSeen.Equal(t0.Add(20*time.Second)) {
		t.Fatalf("first/last = %v/%v", s.FirstSeen, s.LastSeen)
	}
}
