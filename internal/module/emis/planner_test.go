package emis

import "testing"

func TestSimulateZeroRate(t *testing.T) {
	s := Simulate([]Debt{{Name: "Phone", Balance: 1200, Min: 100}}, 0, "scheduled")
	if s.Months != 12 || s.Interest != 0 || s.NeverEnds || len(s.Order) != 1 || s.Order[0].Month != 12 {
		t.Errorf("%+v", s)
	}
}

func TestSimulateNeverEnds(t *testing.T) {
	// 24% on 100000 is 2000 a month; paying 1000 never clears it
	d := []Debt{{Name: "Card", Balance: 100000, Rate: 24, Min: 1000}}
	if !Simulate(d, 0, "scheduled").NeverEnds {
		t.Error("scheduled should never end")
	}
	if s := Simulate(d, 5000, "avalanche"); s.NeverEnds || s.Months == 0 {
		t.Errorf("extra should clear it: %+v", s)
	}
}

func TestPlanStrategies(t *testing.T) {
	big := Debt{ID: "a", Name: "Personal loan", Balance: 100000, Rate: 20, Min: 4000}
	small := Debt{ID: "b", Name: "Phone", Balance: 10000, Rate: 5, Min: 1000}
	p := Plan([]Debt{big, small}, 2000)
	sch, ava, snow := p.Strategies[0], p.Strategies[1], p.Strategies[2]
	if sch.Method != "scheduled" || ava.Method != "avalanche" || snow.Method != "snowball" {
		t.Fatalf("order: %s %s %s", sch.Method, ava.Method, snow.Method)
	}
	if !(ava.Interest <= snow.Interest && snow.Interest < sch.Interest) {
		t.Errorf("interest: avalanche %v snowball %v scheduled %v", ava.Interest, snow.Interest, sch.Interest)
	}
	if ava.Months >= sch.Months || ava.InterestSaved != r2(sch.Interest-ava.Interest) || ava.MonthsSaved != sch.Months-ava.Months {
		t.Errorf("saved: %+v vs %+v", ava, sch)
	}
	if snow.Order[0].Name != "Phone" { // smallest balance goes first
		t.Errorf("snowball cleared %v first", snow.Order[0])
	}
	if sch.InterestSaved != 0 {
		t.Error("scheduled saves nothing against itself")
	}
}

func TestPlanNoDebts(t *testing.T) {
	p := Plan([]Debt{}, 1000)
	if len(p.Strategies) != 3 || p.Strategies[1].Months != 0 || p.Strategies[1].NeverEnds {
		t.Errorf("%+v", p)
	}
}
