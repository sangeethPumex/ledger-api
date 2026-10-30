package portfolio

import (
	"math"
	"testing"
	"time"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/stocks"
)

var stocksEmpty stocks.Portfolio

func at(s string) time.Time { t, _ := day(s); return t }

func TestAllocate(t *testing.T) {
	nav := 10.0
	fs := []funds.Fund{
		{Type: "Equity", Units: 100, Nav: &nav},                    // 1000
		{Type: "Debt", Units: 50, Nav: &nav},                       // 500
		{Type: "Metal", Units: 10, Nav: &nav},                      // 100
		{Type: "Equity", Units: 1000, Nav: &nav, Status: "exited"}, // out
	}
	a := Allocate(fs, 400, 2000, &Target{Equity: 40, Debt: 20, Gold: 10, Cash: 30})
	if a.Total != 4000 {
		t.Fatalf("total %v", a.Total)
	}
	eq := a.Slices[0] // equity: 1000 fund + 400 stock = 1400 of 4000 = 35%
	if eq.Value != 1400 || math.Abs(eq.Pct-35) > 1e-9 || math.Abs(*eq.Drift+5) > 1e-9 || math.Abs(*eq.Shift-200) > 1e-9 {
		t.Fatalf("equity %+v drift %v shift %v", eq, *eq.Drift, *eq.Shift)
	}
	if Allocate(nil, 0, -50, nil).Total != 0 {
		t.Fatal("overdraft must count as zero cash")
	}
}

func TestTargetValidate(t *testing.T) {
	if (Target{50, 30, 10, 10}).Validate() != "" || (Target{50, 30, 10, 20}).Validate() == "" || (Target{110, -10, 0, 0}).Validate() == "" {
		t.Fatal("target validation")
	}
}

func TestSipHealth(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, ist.Loc)
	sip := func(amount float64, changes ...funds.SipChange) *funds.Sip {
		return &funds.Sip{Amount: amount, Day: 5, Changes: changes}
	}
	fs := []funds.Fund{
		{Name: "missed", Membership: "Standalone", JoinedAt: "2024-01-01", Sip: sip(1000)},
		{Name: "logged", Membership: "Standalone", JoinedAt: "2024-01-01", Sip: sip(1500, funds.SipChange{Date: "2026-03-01", From: 1000, To: 1500}),
			Instalments: []funds.Instalment{{Date: "2026-10-05", Amount: 1500}}},
		{Name: "paused", Membership: "Standalone", JoinedAt: "2024-01-01", Sip: &funds.Sip{Amount: 500, Day: 5, Status: "paused"}},
		{Name: "new", Membership: "Standalone", JoinedAt: "2026-10-10", Sip: sip(500)},
		{Name: "gone", Membership: "Standalone", Status: "exited", Sip: sip(500)},
	}
	got := map[string]SipCheck{}
	for _, c := range SipHealth(fs, nil, now) {
		got[c.Name] = c
	}
	if len(got) != 4 {
		t.Fatalf("want 4 checks, got %d", len(got))
	}
	if c := got["missed"]; c.State != "missed" || c.StepUp != "none" {
		t.Errorf("missed: %+v", c)
	}
	if c := got["logged"]; c.State != "ok" || c.StepUp != "up" || math.Abs(*c.StepUpPct-0.5) > 1e-9 {
		t.Errorf("logged: %+v", c)
	}
	if got["paused"].State != "paused" {
		t.Errorf("paused: %+v", got["paused"])
	}
	if c := got["new"]; c.State != "ok" || c.StepUp != "new" {
		t.Errorf("new: %+v", c)
	}
}

func TestRates(t *testing.T) {
	nav := 12.0
	now := at("2026-01-01")
	f := funds.Fund{Name: "f", JoinedAt: "2025-01-01", Invested: 1000, Units: 100, Nav: &nav} // 1000 -> 1200 in a year
	r := Rates([]funds.Fund{f}, &stocksEmpty, now)
	if r.Portfolio == nil || math.Abs(*r.Portfolio-0.20) > 1e-3 || r.Funds[0].Exact {
		t.Fatalf("%+v", r)
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 10, 30, 0, 0, 0, 0, ist.Loc)
	in := Summarize([]row{{"2026-04", "dividend", 100}, {"2026-09", "interest", 50}, {"2026-03", "interest", 70}, {"2025-03", "interest", 9}}, nil, now)
	if in.FY != "2026-27" || in.Dividend != 100 || in.Interest != 50 || in.LastFY != 70 || len(in.Months) != 12 || in.Months[11].YM != "2026-10" {
		t.Fatalf("%+v", in)
	}
}
