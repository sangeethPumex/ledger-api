package stocks

import (
	"math"
	"testing"
	"time"

	"ledger-backend/internal/ist"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCompute(t *testing.T) {
	now := time.Now()
	lots := []Lot{
		{Symbol: "A.NS", Quantity: 10, BuyPrice: 100, BuyDate: "2025-01-01"},
		{Symbol: "A.NS", Quantity: 10, BuyPrice: 200, BuyDate: "2025-02-01"},
		{Symbol: "B.NS", Quantity: 5, BuyPrice: 100, BuyDate: "2025-01-01"}, // no quote: valued at cost
	}
	q := map[string]Quote{"A.NS": {Symbol: "A.NS", Price: 180, PrevClose: 150, FetchedAt: now.Add(-time.Hour)}}
	p := Compute(lots, q, now)
	a := p.Positions[0]
	if a.Symbol != "A.NS" || !near(a.Invested, 3000) || !near(a.AvgPrice, 150) || !near(a.Value, 3600) || !near(a.Pnl, 600) || !near(a.PnlPct, 0.2) {
		t.Errorf("position A: %+v", a)
	}
	if !near(a.DayChange, 600) || !near(a.DayPct, 0.2) || !a.Stale {
		t.Errorf("day/stale: %+v", a)
	}
	if !near(p.Summary.Value, 4100) || !near(p.Summary.Invested, 3500) || p.Summary.Unpriced != 1 || !near(a.Allocation, 3600.0/4100) {
		t.Errorf("summary: %+v", p.Summary)
	}
	if a.Lots[0].BuyDate != "2025-01-01" || !near(a.Lots[1].Pnl, -200) {
		t.Errorf("lots: %+v", a.Lots)
	}
	if e := Compute(nil, nil, now); len(e.Positions) != 0 || e.Summary.Value != 0 {
		t.Errorf("empty: %+v", e)
	}
}

func TestFreshAt(t *testing.T) {
	at := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, ist.Loc) } // 26 Oct 2026 is a Monday
	if got := freshAt(at(26, 11, 0)); !got.Equal(at(26, 10, 50)) {
		t.Errorf("open: %v", got)
	}
	if got := freshAt(at(26, 18, 0)); !got.Equal(at(26, 15, 45)) {
		t.Errorf("after close: %v", got)
	}
	if got := freshAt(at(26, 8, 0)); !got.Equal(at(23, 15, 45)) { // Monday pre-open -> Friday's close
		t.Errorf("pre-open: %v", got)
	}
	if got := freshAt(at(25, 12, 0)); !got.Equal(at(23, 15, 45)) { // Sunday
		t.Errorf("weekend: %v", got)
	}
}

func TestValidate(t *testing.T) {
	now := time.Now()
	in := Input{Symbol: " reliance.ns ", Quantity: 2, BuyPrice: 10.5, BuyDate: "2025-01-02"}
	if m := in.Validate(now); m != "" || in.Symbol != "RELIANCE.NS" {
		t.Fatalf("rejected: %q %q", m, in.Symbol)
	}
	for name, bad := range map[string]Input{
		"suffix": {Symbol: "TCS", Quantity: 1, BuyPrice: 1, BuyDate: "2025-01-02"},
		"path":   {Symbol: "../X.NS", Quantity: 1, BuyPrice: 1, BuyDate: "2025-01-02"},
		"qty":    {Symbol: "TCS.NS", BuyPrice: 1, BuyDate: "2025-01-02"},
		"date":   {Symbol: "TCS.NS", Quantity: 1, BuyPrice: 1, BuyDate: "02-01-2025"},
		"future": {Symbol: "TCS.NS", Quantity: 1, BuyPrice: 1, BuyDate: "2999-01-01"},
	} {
		if bad.Validate(now) == "" {
			t.Errorf("%s accepted", name)
		}
	}
}
