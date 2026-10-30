package alerts

import "testing"

func TestHit(t *testing.T) {
	for _, c := range []struct {
		op           string
		level, price float64
		want         bool
	}{
		{"above", 100, 100, true}, {"above", 100, 99.99, false}, {"below", 100, 100, true}, {"below", 100, 100.01, false},
	} {
		if got := (&Alert{Op: c.op, Price: c.level}).Hit(c.price); got != c.want {
			t.Errorf("%s %v at %v: got %v", c.op, c.level, c.price, got)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Input{Kind: "stock", Ref: " reliance.ns ", Op: "above", Price: 10}
	if ok.Validate() != "" || ok.Ref != "RELIANCE.NS" {
		t.Fatalf("valid input rejected: %+v", ok)
	}
	for _, in := range []Input{
		{"stock", "RELIANCE", "above", 10}, {"fund", "12ab", "above", 10}, {"fund", "119551", "up", 10}, {"fund", "119551", "below", 0},
	} {
		if in.Validate() == "" {
			t.Errorf("accepted %+v", in)
		}
	}
}
