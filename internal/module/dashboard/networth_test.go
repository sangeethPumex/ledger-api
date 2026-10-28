package dashboard

import (
	"testing"
	"time"
)

func TestProject(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	from := newPoint("2026-10-28", 100000, 200000, 50000)
	sc := project(from, Upcoming{MonthlyNet: 10000, SipMonthly: 5000, EmiMonthly: 20000, AvgDailySpend: 100}, now, 3)
	if len(sc) != 3 || len(sc[0].Points) != 3 {
		t.Fatalf("shape %+v", sc)
	}
	last := func(i int) int64 { return sc[i].Points[2].NetWorth }
	if !(last(1) < last(0) && last(0) < last(2)) {
		t.Errorf("want conservative < expected < optimistic, got %d %d %d", last(1), last(0), last(2))
	}
	if p := sc[0].Points[2]; *p.Owed != 0 || p.NetWorth != *p.Cash+*p.Investments {
		t.Errorf("owed should floor at 0 and stay out of net worth: %+v", p)
	}
	if sc[0].Points[0].Date != "2026-11-28" {
		t.Errorf("date %s", sc[0].Points[0].Date)
	}
}

func TestLevels(t *testing.T) {
	got := levels([]Day{{Paise: 0}, {Paise: 100}, {Paise: 200}, {Paise: 300}, {Paise: 400}, {Paise: 100000}})
	want := []int{0, 1, 2, 3, 4, 4}
	for i, w := range want {
		if got[i].Level != w {
			t.Errorf("day %d: level %d want %d", i, got[i].Level, w)
		}
	}
}

func TestLevelsFewBusyDays(t *testing.T) {
	for n := 0; n <= 3; n++ { // used to index sums[-1] and panic the request
		days := make([]Day, 10)
		for i := 0; i < n; i++ {
			days[i].Paise = int64(100 * (i + 1))
		}
		got := levels(days)
		for i := 0; i < n; i++ {
			if got[i].Level < 1 || got[i].Level > 4 {
				t.Errorf("%d busy days: day %d has level %d", n, i, got[i].Level)
			}
		}
		for i := 1; i < n; i++ {
			if got[i].Level < got[i-1].Level {
				t.Errorf("%d busy days: a bigger day got a lower level", n)
			}
		}
	}
}
