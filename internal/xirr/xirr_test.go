package xirr

import (
	"math"
	"testing"
	"time"
)

func d(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }

func TestRate(t *testing.T) {
	// 1000 in, 1100 back exactly 365 days later: 10%.
	r, ok := Rate([]Flow{{d(2025, 1, 1), -1000}, {d(2026, 1, 1), 1100}})
	if !ok || math.Abs(r-0.10) > 1e-6 {
		t.Fatalf("got %v %v, want 0.10", r, ok)
	}
	// A loss is a negative rate.
	if r, ok := Rate([]Flow{{d(2025, 1, 1), -1000}, {d(2026, 1, 1), 900}}); !ok || math.Abs(r+0.10) > 1e-6 {
		t.Fatalf("loss: got %v %v", r, ok)
	}
	// Excel's documented XIRR example.
	r, ok = Rate([]Flow{{d(2008, 1, 1), -10000}, {d(2008, 3, 1), 2750}, {d(2008, 10, 30), 4250}, {d(2009, 2, 15), 3250}, {d(2009, 4, 1), 2750}})
	if !ok || math.Abs(r-0.373362535) > 1e-4 {
		t.Fatalf("excel: got %v", r)
	}
	for name, fl := range map[string][]Flow{
		"one sign": {{d(2025, 1, 1), -1000}, {d(2026, 1, 1), -5}},
		"too short": {{d(2025, 1, 1), -1000}, {d(2025, 1, 8), 1010}},
	} {
		if _, ok := Rate(fl); ok {
			t.Errorf("%s: want no rate", name)
		}
	}
}
