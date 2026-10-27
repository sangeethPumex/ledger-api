package emis

import (
	"math"
	"testing"
)

func f(v float64) *float64 { return &v }

func TestValidate(t *testing.T) {
	bads := map[string]Input{
		"no name":      {Kind: "loan", Amount: 1, DueDay: 1, Total: 1},
		"bad kind":     {Name: "x", Kind: "gift", Amount: 1, DueDay: 1, Total: 1},
		"bad day":      {Name: "x", Kind: "loan", Amount: 1, DueDay: 32, Total: 1},
		"paid>total":   {Name: "x", Kind: "loan", Amount: 1, DueDay: 1, Total: 2, Paid: 3},
		"card nobal":   {Name: "x", Kind: "card", Amount: 1, DueDay: 1},
		"loan notot":   {Name: "x", Kind: "loan", Amount: 1, DueDay: 1},
		"negative bal": {Name: "x", Kind: "loan", Amount: 1, DueDay: 1, Total: 1, Balance: f(-1)},
	}
	for name, in := range bads {
		if in.Validate() == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, in := range []Input{
		{Name: "Car", Kind: "loan", Amount: 5000, DueDay: 5, Total: 60, Paid: 12, Rate: f(9)},
		{Name: "Card", Kind: "card", Amount: 500, DueDay: 5, Balance: f(10000)},
	} {
		if m := in.Validate(); m != "" {
			t.Errorf("%s rejected: %s", in.Name, m)
		}
	}
}

func TestPaymentsAndImpact(t *testing.T) {
	e := &Emi{Kind: "loan", Amount: 5000, Total: 60, Paid: 0, Rate: f(10), Balance: f(100000)}
	p, i := Split(e)
	if math.Abs(i-833.33) > 0.01 || math.Abs(p-4166.67) > 0.01 {
		t.Fatalf("split %v %v", p, i)
	}
	Apply(e, Payment{Kind: "emi", Amount: 5000, Principal: p, Interest: i})
	if e.Paid != 1 || math.Abs(*e.Balance-95833.33) > 0.01 {
		t.Fatalf("after emi: paid %d bal %v", e.Paid, *e.Balance)
	}
	Apply(e, Payment{Kind: "extra", Amount: 10000, Principal: 10000})
	if e.Paid != 1 {
		t.Error("extra payment counted as an instalment")
	}
	if !Undo(e) || !Undo(e) || e.Paid != 0 || math.Abs(*e.Balance-100000) > 0.01 || Undo(e) {
		t.Fatalf("undo: paid %d bal %v", e.Paid, *e.Balance)
	}

	m, _, ok := Payoff(100000, 10, 5000)
	if !ok || m != 22 {
		t.Errorf("payoff months %d ok %v", m, ok)
	}
	if _, _, ok := Payoff(100000, 10, 500); ok {
		t.Error("payment below interest should not pay off")
	}
	saved, months, _, ok := Impact(e, 10000)
	if !ok || saved <= 0 || months < 1 {
		t.Errorf("impact %v %v %v", saved, months, ok)
	}
	if _, _, _, ok := Impact(&Emi{Kind: "purchase", Amount: 1}, 1); ok {
		t.Error("impact without a loan rate")
	}
}
