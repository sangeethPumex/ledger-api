package funds

import (
	"math"
	"testing"
	"time"
)

func TestValidateAndSip(t *testing.T) {
	ok := Input{Code: 140084, Platform: "Groww", Type: "Equity", Membership: "Standalone", Units: 10, AvgNav: 100}
	if m := ok.Validate(); m != "" {
		t.Fatalf("rejected: %s", m)
	}
	if ok.invested() != 1000 {
		t.Errorf("invested default %v", ok.invested())
	}
	for name, in := range map[string]Input{
		"type":       {Code: 1, Platform: "p", Type: "Gold", Membership: "Genius", Units: 1, AvgNav: 1},
		"membership": {Code: 1, Platform: "p", Type: "Debt", Membership: "VIP", Units: 1, AvgNav: 1},
		"units":      {Code: 1, Platform: "p", Type: "Debt", Membership: "Genius", AvgNav: 1},
		"sip day":    {Code: 1, Platform: "p", Type: "Debt", Membership: "Genius", Units: 1, AvgNav: 1, Sip: &Sip{Amount: 5, Day: 0}},
	} {
		if in.Validate() == "" {
			t.Errorf("%s accepted", name)
		}
	}

	nav := 200.0
	f := &Fund{Units: 10, Invested: 1000, AvgNav: 100, Nav: &nav, Sip: &Sip{Amount: 2000, Day: 5}}
	if !AddSip(f) || math.Abs(f.Units-20) > 1e-9 || f.Invested != 3000 || math.Abs(f.AvgNav-150) > 1e-9 {
		t.Errorf("sip: %+v", f)
	}
	if AddSip(&Fund{Sip: &Sip{Amount: 1, Day: 1}}) {
		t.Error("sip without nav accepted")
	}
	if v := Value(&Fund{Units: 2, Invested: 50}); v != 50 {
		t.Errorf("value without nav %v", v)
	}
}

func TestSipStatus(t *testing.T) {
	nav := 100.0
	for _, st := range []string{"paused", "cancelled"} {
		f := &Fund{Units: 1, Invested: 100, AvgNav: 100, Nav: &nav, Sip: &Sip{Amount: 100, Day: 5, Status: st}}
		if AddSip(f) || ViewOf(*f, time.Now()).SipInDays != nil {
			t.Errorf("%s sip still live", st)
		}
	}
	in := Input{Code: 1, Platform: "p", Type: "Debt", Membership: "Genius", Units: 1, AvgNav: 1, Sip: &Sip{Amount: 5, Day: 1, Status: "stopped"}}
	if in.Validate() == "" {
		t.Error("bad status accepted")
	}
	in.Sip.Status, in.AccountID = "paused", "nope"
	if in.Validate() == "" {
		t.Error("bad account id accepted")
	}
}

func TestExitAndGenius(t *testing.T) {
	nav := 120.0
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	out := &Fund{Name: "Out", Membership: "Genius", Units: 10, Invested: 1000, AvgNav: 100, Nav: &nav, Sip: &Sip{Amount: 500, Day: 5}, JoinedAt: "2026-06-01"}
	in := &Fund{Name: "In", Membership: "Genius", Units: 5, Invested: 500, AvgNav: 100, Nav: &nav, JoinedAt: "2026-10-20"}
	other := &Fund{Name: "Standalone", Membership: "Standalone", Units: 1, Invested: 100, Nav: &nav}

	Exit(out, "2026-10-25", 1150)
	if !out.Exited() || out.Sip.Status != "cancelled" || AddSip(out) {
		t.Errorf("exit should end the SIP: %+v", out)
	}
	v := ViewOf(*out, now)
	if v.Active || v.Value != 1150 || v.Pnl != 150 || v.SipInDays != nil {
		t.Errorf("exited view: active %v value %v pnl %v", v.Active, v.Value, v.Pnl)
	}

	g := GeniusOf([]Fund{*out, *in, *other}, now)
	if g.Active != 1 || g.Exited != 1 || g.Value != 600 || g.Invested != 500 || g.Realised != 150 {
		t.Errorf("genius totals: %+v", g)
	}
	if g.LastRebalance != "2026-10-25" || len(g.Moves) != 2 || g.Moves[0].Kind != "out" || g.Moves[1].Name != "In" {
		t.Errorf("genius moves: %+v", g)
	}

	Reenter(out)
	if out.Exited() || out.Proceeds != nil || out.ExitedAt != "" {
		t.Errorf("reenter: %+v", out)
	}
	if (&ExitReq{Date: "25-10-2026"}).Validate() == "" {
		t.Error("bad date accepted")
	}
	neg := -1.0
	if (&ExitReq{Proceeds: &neg}).Validate() == "" {
		t.Error("negative proceeds accepted")
	}
}

func TestGeniusSip(t *testing.T) {
	nav := 100.0
	a := &Fund{Name: "A", Membership: "Genius", Units: 30, Invested: 2500, AvgNav: 83.33, Nav: &nav} // worth 3000
	b := &Fund{Name: "B", Membership: "Genius", Units: 10, Invested: 900, AvgNav: 90, Nav: &nav}     // worth 1000
	gone := &Fund{Name: "Gone", Membership: "Genius", Units: 5, Invested: 400, Nav: &nav, Status: "exited", Proceeds: new(float64)}
	solo := &Fund{Name: "Solo", Membership: "Standalone", Units: 1, Invested: 100, Nav: &nav}
	sip := &Sip{Amount: 4000, Day: 5}

	if !AddGeniusSip([]*Fund{a, b, gone, solo}, sip) {
		t.Fatal("not booked")
	}
	if a.Invested != 5500 || b.Invested != 1900 || solo.Invested != 100 || gone.Invested != 400 {
		t.Errorf("split by value should give A 3000, B 1000: a %v b %v", a.Invested, b.Invested)
	}
	if math.Abs(a.Units-60) > 1e-9 || math.Abs(b.Units-20) > 1e-9 {
		t.Errorf("units: a %v b %v", a.Units, b.Units)
	}

	// the same basket SIP buys the same total with two funds or five
	total := func(n int) float64 {
		var fs []*Fund
		for i := 0; i < n; i++ {
			fs = append(fs, &Fund{Membership: "Genius", Units: 1, Invested: 100, Nav: &nav})
		}
		AddGeniusSip(fs, sip)
		var sum float64
		for _, f := range fs {
			sum += f.Invested - 100
		}
		return sum
	}
	if math.Abs(total(2)-4000) > 1e-6 || math.Abs(total(5)-4000) > 1e-6 {
		t.Errorf("basket SIP must stay 4000: %v / %v", total(2), total(5))
	}

	noNav := &Fund{Membership: "Genius", Units: 1, Invested: 100}
	if AddGeniusSip([]*Fund{a, noNav}, sip) || AddGeniusSip(nil, sip) || AddGeniusSip([]*Fund{a}, &Sip{Amount: 1, Day: 1, Status: "paused"}) {
		t.Error("should refuse without a NAV, a fund or a live SIP")
	}

	// a Genius fund has no SIP of its own
	own := Fund{Membership: "Genius", Sip: &Sip{Amount: 500, Day: 5}, Nav: &nav, Units: 1, Invested: 100}
	if v := ViewOf(own, time.Now()); v.Sip != nil || v.SipInDays != nil || AddSip(&own) {
		t.Error("genius fund kept a per-fund SIP")
	}
	in := Input{Code: 1, Platform: "ET Money", Type: "Equity", Membership: "Genius", Units: 1, AvgNav: 1, Sip: &Sip{Amount: 5, Day: 1}}
	if in.Validate() == "" {
		t.Error("per-fund SIP on a Genius fund accepted")
	}
	if (&GeniusSip{Sip: Sip{Amount: 4000, Day: 5}}).Validate() != "" || (&GeniusSip{Sip: Sip{Amount: 0, Day: 5}}).Validate() == "" {
		t.Error("genius sip validation")
	}
}
