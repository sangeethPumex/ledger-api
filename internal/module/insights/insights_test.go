package insights

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

var now = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func pts(paise int64, daysAgo ...int) []point {
	out := []point{}
	for _, d := range daysAgo { // oldest first
		out = append(out, point{ID: bson.NewObjectID(), At: now.AddDate(0, 0, -d), Paise: paise})
	}
	return out
}

func TestDetectRecurring(t *testing.T) {
	vid := bson.NewObjectID()
	all := []vendorSeries{
		{Name: "Netflix", VendorID: &vid, Txns: pts(64900, 92, 61, 31, 1)},            // monthly, steady
		{Name: "Swiggy", Txns: pts(30000, 20, 17, 11, 9, 2)},                          // frequent, not periodic
		{Name: "Gym", Txns: pts(150000, 400, 365, 335, 305, 275, 245, 215, 185, 155)}, // monthly but a long-lapsed cycle
		{Name: "Once", Txns: pts(5000, 10)},
		{Name: "Insurance", Txns: pts(1200000, 366, 1)}, // yearly, two charges
	}
	got := detectRecurring(all, now)
	by := map[string]Recurring{}
	for _, r := range got {
		by[r.Name] = r
	}
	if n := by["Netflix"]; n.Cadence != "monthly" || n.AmountPaise != 64900 || n.Status != "active" || n.MonthlyPaise != 64900 {
		t.Errorf("Netflix = %+v", n)
	}
	if _, ok := by["Swiggy"]; ok {
		t.Error("Swiggy is frequent, not recurring")
	}
	if _, ok := by["Once"]; ok {
		t.Error("a single charge is not recurring")
	}
	if g := by["Gym"]; g.Status != "lapsed" {
		t.Errorf("Gym should be lapsed, got %+v", g)
	}
	if y := by["Insurance"]; y.Cadence != "yearly" || y.MonthlyPaise != 100000 {
		t.Errorf("Insurance = %+v", y)
	}
	if got[0].Status != "active" {
		t.Error("active charges sort first")
	}
}

func TestDetectAnomalies(t *testing.T) {
	vid := bson.NewObjectID()
	spike := pts(20000, 60, 45, 30, 15)
	spike = append(spike, point{ID: bson.NewObjectID(), At: now.AddDate(0, 0, -1), Paise: 840000})
	all := []vendorSeries{
		{Name: "Zomato", VendorID: &vid, Txns: spike},
		{Name: "unknown@upi", Txns: pts(900000, 2)},
		{Name: "Small", Txns: pts(5000, 1)},  // unknown but tiny
		{Name: "Old", Txns: pts(900000, 40)}, // unknown and big, but not recent
		{Name: "Steady", VendorID: &vid, Txns: pts(20000, 60, 40, 20, 1)},
	}
	got := detectAnomalies(all, now)
	if len(got) != 2 {
		t.Fatalf("want 2 anomalies, got %d: %+v", len(got), got)
	}
	if got[0].Vendor != "Zomato" || got[0].Kind != "spike" || got[0].Ratio != 42 {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Kind != "new_vendor" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestInr(t *testing.T) {
	for in, want := range map[int64]string{0: "₹0", 99900: "₹999", 100000: "₹1,000", 840000: "₹8,400", 12345600: "₹1,23,456"} {
		if got := inr(in); got != want {
			t.Errorf("inr(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestCommentary(t *testing.T) {
	var p Patterns
	p.Weekdays[6] = Bucket{Paise: 26 * 300000, Count: 100} // Saturdays: ₹3,000 each
	p.Weekdays[2] = Bucket{Paise: 26 * 20000, Count: 30}
	p.Hours[23] = Bucket{Paise: 26 * 300000}
	p.Hours[13] = Bucket{Paise: 26 * 20000}
	occ := [7]int{26, 26, 26, 26, 26, 26, 26}
	notes := commentary(&p, occ, 182)
	if len(notes) != 4 {
		t.Fatalf("want 4 notes, got %q", notes)
	}
	if want := "Saturdays are your heaviest day: ₹3,000 on average, 6.6x a typical day."; notes[0] != want {
		t.Errorf("notes[0] = %q, want %q", notes[0], want)
	}
	if got := commentary(&Patterns{}, occ, 182); len(got) != 0 {
		t.Errorf("no spending, no notes; got %q", got)
	}
}
