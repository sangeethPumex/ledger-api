package emis

import (
	"testing"
	"time"
)

func carLoan() *Emi {
	rate, bal := 7.95, 687315.0
	return &Emi{Kind: "loan", Amount: 11578, DueDay: 23, Rate: &rate, Balance: &bal, RateType: "variable", Ledger: carLedger,
		Rates: []RateChange{{Rate: 7.70}, {From: "2026-09-23", Rate: 7.95}}}
}

// The seeded statement must agree with itself and with the totals the lender printed.
func TestCarLedgerAddsUp(t *testing.T) {
	var tot Totals
	bal := 750000.0
	for _, p := range carLedger {
		if r2(p.EMI+p.Extra-p.Interest) != p.Principal || r2(bal-p.Principal) != p.Closing {
			t.Fatalf("row %s does not add up", p.To)
		}
		bal = p.Closing
		add(&tot, p)
	}
	if tot != (Totals{Interest: 41667, EMI: 103202, Extra: 1150, Principal: 62685}) {
		t.Errorf("totals %+v", tot)
	}
}

func TestBuildScheduleProjectsToZero(t *testing.T) {
	s := BuildSchedule(carLoan(), time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
	if !s.Available || len(s.Rows) != 9+s.Months {
		t.Fatalf("rows %d months %d", len(s.Rows), s.Months)
	}
	first := s.Rows[9]
	if !first.Future || first.From != "2026-10-23" || first.To != "2026-11-22" {
		t.Errorf("first future row %+v", first.Period)
	}
	// 687315 x 7.95% x 31/365 = 4640.7
	if first.PaidOn != "2026-11-23" || s.Rows[0].PaidOn != "2026-02-23" || s.Rows[8].PaidOn != "2026-10-23" {
		t.Errorf("paid on %s %s %s", s.Rows[0].PaidOn, s.Rows[8].PaidOn, first.PaidOn)
	}
	if first.Interest < 4640 || first.Interest > 4642 {
		t.Errorf("interest %v", first.Interest)
	}
	if last := s.Rows[len(s.Rows)-1]; last.Closing != 0 || last.EMI > 11578 {
		t.Errorf("last row %+v", last)
	}
	if s.Months < 75 || s.Months > 77 {
		t.Errorf("months left %d", s.Months) // annuity formula: ln(1/(1-B*r/E))/ln(1+r) = 75.7
	}
	if s.Rows[0].Rate != 7.70 || s.Rows[8].Rate != 7.95 {
		t.Errorf("rates %v %v", s.Rows[0].Rate, s.Rows[8].Rate)
	}
}

func TestHigherRateLengthensLoan(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	a := BuildSchedule(carLoan(), now)
	hi := carLoan()
	setRates(hi, RateChange{From: "2026-10-01", Rate: 10}, now)
	if b := BuildSchedule(hi, now); b.Months <= a.Months {
		t.Errorf("months %d vs %d", b.Months, a.Months)
	}
	setRates(hi, RateChange{From: "2026-10-02", Rate: 20}, now)
	if !BuildSchedule(hi, now).NeverEnds {
		t.Error("an EMI below the interest should never clear")
	}
}

func TestRateChangeDated(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	e := carLoan()
	setRates(e, RateChange{From: "2027-01-01", Rate: 9}, now)
	if *e.Rate != 7.95 || len(e.Rates) != 3 {
		t.Fatalf("a future change must not take effect yet: %v %+v", *e.Rate, e.Rates)
	}
	s := BuildSchedule(e, now)
	for _, r := range s.Rows {
		if r.Future && ((r.From < "2027-01-01") != (r.Rate == 7.95)) {
			t.Fatalf("row %s uses %v", r.From, r.Rate)
		}
	}
	setRates(e, RateChange{From: "2026-10-01", Rate: 8.2}, now)
	if *e.Rate != 8.2 {
		t.Errorf("a past-dated change is in force now: %v", *e.Rate)
	}
}
