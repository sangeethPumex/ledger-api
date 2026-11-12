package habits

import (
	"testing"
	"time"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/grocery-pantry-tracker/orders"
)

func food(at time.Time, dish string, paise int64) orders.Order {
	return orders.Order{Source: orders.SourceFood, OrderID: at.String() + dish, PlacedAt: at, Status: "Delivered", Store: "Paradise", TotalPaise: paise, Items: []orders.Item{{Name: dish, Qty: 1}}}
}

func TestFindFridayBiryani(t *testing.T) {
	now := time.Date(2026, 11, 12, 15, 0, 0, 0, ist.Loc) // a Thursday
	var all []orders.Order
	for _, d := range []int{6, 30, 23, 16, 9, 2} { // six of the eight Fridays since Sep 18, at about 9 pm
		m := time.October
		if d == 6 {
			m = time.November
		}
		all = append(all, food(time.Date(2026, m, d, 21, 10, 0, 0, ist.Loc), "Chicken Biryani", 42000))
	}
	all = append(all, food(time.Date(2026, 9, 18, 13, 0, 0, 0, ist.Loc), "Masala Dosa", 15000)) // one-off, and sets the span to 8 weeks
	all = append(all, food(time.Date(2026, 11, 8, 12, 0, 0, 0, ist.Loc), "Masala Dosa", 15000))

	got := Find(all, now)
	if len(got) != 1 {
		t.Fatalf("want one habit, got %+v", got)
	}
	h := got[0]
	if h.Count != 6 || h.Slots != 8 || h.AvgPaise != 42000 || h.MinuteOfDay != 21*60+10 || h.Window != 21 || h.Weekday != int(time.Friday) {
		t.Errorf("habit %+v", h)
	}
	if h.MonthlyPaise < 136000 || h.MonthlyPaise > 137000 { // 420 * 6/8 * 4.33
		t.Errorf("monthly %d", h.MonthlyPaise)
	}
	if want := time.Date(2026, 11, 13, 21, 10, 0, 0, ist.Loc); !NextAt(h, now).Equal(want) {
		t.Errorf("next %v, want %v", NextAt(h, now), want)
	}
	// Past the usual time on the day, the next one is a week on.
	late := time.Date(2026, 11, 13, 22, 0, 0, 0, ist.Loc)
	if want := time.Date(2026, 11, 20, 21, 10, 0, 0, ist.Loc); !NextAt(h, late).Equal(want) {
		t.Errorf("next after the time %v, want %v", NextAt(h, late), want)
	}
}

func TestFindIgnoresRareAndCancelled(t *testing.T) {
	now := time.Date(2026, 11, 12, 15, 0, 0, 0, ist.Loc)
	rare := []orders.Order{food(time.Date(2026, 11, 6, 21, 0, 0, 0, ist.Loc), "Pizza", 30000), food(time.Date(2026, 10, 30, 21, 0, 0, 0, ist.Loc), "Pizza", 30000)}
	if got := Find(rare, now); len(got) != 0 {
		t.Errorf("two orders are not a habit: %+v", got)
	}
	var off []orders.Order
	for _, d := range []int{6, 30, 23} {
		o := food(time.Date(2026, 10, d, 21, 0, 0, 0, ist.Loc), "Pizza", 30000)
		o.Status = "CANCELLED"
		off = append(off, o)
	}
	if got := Find(off, now); len(got) != 0 {
		t.Errorf("cancelled orders counted: %+v", got)
	}
}
