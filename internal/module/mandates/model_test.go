package mandates

import (
	"slices"
	"testing"
	"time"

	"ledger-backend/internal/ist"
)

func d(s string) time.Time { t, _ := time.ParseInLocation(day, s, ist.Loc); return t }

func TestOccurrences(t *testing.T) {
	now := d("2026-10-28")
	end := now.AddDate(1, 0, 0)
	cases := []struct {
		name string
		m    Mandate
		want []string
	}{
		{"one-off in range", Mandate{Frequency: "one-off", NextDue: d("2026-12-01")}, []string{"2026-12-01"}},
		{"one-off passed", Mandate{Frequency: "one-off", NextDue: d("2026-10-01")}, nil},
		{"yearly rolls past", Mandate{Frequency: "yearly", NextDue: d("2024-03-15")}, []string{"2027-03-15"}},
		{"quarterly", Mandate{Frequency: "quarterly", NextDue: d("2026-11-30")}, []string{"2026-11-30", "2027-02-28", "2027-05-30", "2027-08-30"}},
		{"monthly clamps short months", Mandate{Frequency: "monthly", NextDue: d("2026-10-31")}, []string{"2026-10-31", "2026-11-30", "2026-12-31", "2027-01-31", "2027-02-28", "2027-03-31", "2027-04-30", "2027-05-31", "2027-06-30", "2027-07-31", "2027-08-31", "2027-09-30"}},
	}
	for _, c := range cases {
		var got []string
		for _, o := range c.m.Occurrences(now, end) {
			got = append(got, o.Format(day))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestBuildPlan(t *testing.T) {
	now := d("2026-10-28")
	p := BuildPlan([]Mandate{
		{Name: "Insurance", AmountPaise: 1_200_000, Frequency: "yearly", NextDue: d("2026-12-10")},
		{Name: "Netflix", AmountPaise: 64_900, Frequency: "monthly", NextDue: d("2026-11-02")},
	}, now)
	if p.Next30 != 64_900 || p.Next90 != 1_200_000+3*64_900 {
		t.Fatalf("next30=%d next90=%d", p.Next30, p.Next90)
	}
	if p.SetAside != 100_000+64_900 {
		t.Fatalf("set aside = %d", p.SetAside)
	}
	if p.Calendar[0].Name != "Netflix" || p.Items[0].Name != "Netflix" || p.Items[1].Days != 43 {
		t.Fatalf("order/days wrong: %+v", p.Items)
	}
}

func TestDetect(t *testing.T) {
	now := d("2026-10-28")
	monthly := []Sample{{"Netflix", 64900, d("2026-08-05")}, {"Netflix", 64900, d("2026-09-05")}, {"Netflix", 64900, d("2026-10-05")}}
	swiggy := []Sample{{"Swiggy", 25000, d("2026-08-05")}, {"Swiggy", 90000, d("2026-09-07")}, {"Swiggy", 31000, d("2026-10-02")}}
	yearly := []Sample{{"LIC", 1_200_000, d("2025-12-10")}, {"LIC", 1_200_000, d("2024-12-10")}}
	lapsed := []Sample{{"Gym", 100000, d("2026-03-01")}, {"Gym", 100000, d("2026-04-01")}, {"Gym", 100000, d("2026-05-01")}}
	var all []Sample
	for _, g := range [][]Sample{monthly, swiggy, yearly, lapsed} {
		all = append(all, g...)
	}

	got := Detect(all, nil, now)
	if len(got) != 2 || got[0].Name != "Netflix" || got[0].NextDue != "2026-11-05" || got[1].Name != "LIC" || got[1].Frequency != "yearly" || got[1].NextDue != "2026-12-10" {
		t.Fatalf("got %+v", got)
	}
	if got := Detect(all, []string{"netflix premium"}, now); len(got) != 1 {
		t.Fatalf("known name not skipped: %+v", got)
	}
}

func TestParse(t *testing.T) {
	ok := Input{Name: " Insurance ", AmountPaise: 100, Frequency: "yearly", NextDue: "2026-12-10"}
	if m, msg := ok.Parse(); msg != "" || m.Name != "Insurance" {
		t.Fatalf("valid input rejected: %q", msg)
	}
	for _, bad := range []Input{
		{AmountPaise: 100, Frequency: "yearly", NextDue: "2026-12-10"},
		{Name: "x", Frequency: "yearly", NextDue: "2026-12-10"},
		{Name: "x", AmountPaise: 100, Frequency: "weekly", NextDue: "2026-12-10"},
		{Name: "x", AmountPaise: 100, Frequency: "yearly", NextDue: "10/12/2026"},
		{Name: "x", AmountPaise: 100, Frequency: "yearly", NextDue: "2026-12-10", AccountID: "nope"},
	} {
		if _, msg := bad.Parse(); msg == "" {
			t.Errorf("accepted %+v", bad)
		}
	}
}
