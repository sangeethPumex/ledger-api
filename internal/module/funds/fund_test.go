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
