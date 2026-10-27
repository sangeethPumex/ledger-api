package cards

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/emis"
)

func at(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, ist.Loc) }

func TestCycleStart(t *testing.T) {
	for _, tc := range []struct {
		billing int
		now     time.Time
		want    string
	}{
		{15, at(2026, 10, 20), "2026-10-15"},
		{15, at(2026, 10, 15), "2026-10-15"}, // statement day itself starts the cycle
		{15, at(2026, 10, 3), "2026-09-15"},
		{31, at(2026, 3, 5), "2026-02-28"}, // short month clamps
		{0, at(2026, 10, 20), "2026-10-01"},
	} {
		if got := CycleStart(Card{BillingDay: tc.billing}, tc.now).Format("2006-01-02"); got != tc.want {
			t.Errorf("billing %d at %s: got %s, want %s", tc.billing, tc.now.Format("2006-01-02"), got, tc.want)
		}
	}
}

func TestCompute(t *testing.T) {
	linked := []emis.Emi{
		{ID: bson.NewObjectID(), Name: "Phone", Kind: "purchase", Amount: 2000, Total: 6, Paid: 2}, // 8000 left
		{ID: bson.NewObjectID(), Name: "Done", Kind: "purchase", Amount: 1000, Total: 3, Paid: 3},  // closed: ignored
	}
	c := Card{ID: bson.NewObjectID(), Name: "HDFC", LimitPaise: 100_000_00, BillingDay: 15, DueDay: 5}
	v := Compute(c, 20_000_00, 7, linked, at(2026, 10, 20))
	if v.Kind != KindCredit {
		t.Errorf("kind %q, want credit by default", v.Kind)
	}
	if v.LinkedLeftPaise != 8000_00 || v.LinkedMonthlyPaise != 2000_00 || len(v.LinkedEmis) != 2 {
		t.Errorf("linked: left %d monthly %d n %d", v.LinkedLeftPaise, v.LinkedMonthlyPaise, len(v.LinkedEmis))
	}
	if v.UsedPaise != 28_000_00 || *v.AvailablePaise != 72_000_00 || *v.Utilisation != 0.28 {
		t.Errorf("used %d available %d util %v", v.UsedPaise, *v.AvailablePaise, *v.Utilisation)
	}
	if *v.DaysToBilling != 26 || *v.DaysToDue != 16 {
		t.Errorf("days to billing %d, to due %d", *v.DaysToBilling, *v.DaysToDue)
	}

	manual := int64(50_000_00)
	c.OutstandingPaise = &manual
	if v := Compute(c, 20_000_00, 7, linked, at(2026, 10, 20)); v.UsedPaise != manual {
		t.Errorf("manual outstanding should win, got %d", v.UsedPaise)
	}

	over := Compute(Card{LimitPaise: 1000_00}, 5000_00, 1, nil, at(2026, 10, 20))
	if *over.AvailablePaise != 0 || *over.Utilisation != 5 {
		t.Errorf("over limit: available %d util %v", *over.AvailablePaise, *over.Utilisation)
	}
	if nl := Compute(Card{}, 100, 1, nil, at(2026, 10, 20)); nl.AvailablePaise != nil || nl.Utilisation != nil || nl.DaysToDue != nil {
		t.Error("no limit or days should leave those figures out")
	}
}

func TestValidate(t *testing.T) {
	neg := int64(-1)
	bads := []Card{
		{Name: " "}, {Name: "x", Kind: "debit"}, {Name: "x", Last4: "12a4"}, {Name: "x", LimitPaise: -1},
		{Name: "x", DueDay: 32}, {Name: "x", BillingDay: -1}, {Name: "x", OutstandingPaise: &neg},
	}
	for i, c := range bads {
		if c.Validate() == "" {
			t.Errorf("bad card %d accepted", i)
		}
	}
	if m := (&Card{Name: "Amazon Pay Later", Kind: KindPayLater, LimitPaise: 50_000_00, BillingDay: 1, DueDay: 10}).Validate(); m != "" {
		t.Errorf("good card rejected: %s", m)
	}
	if (&Patch{BillingDay: new(int)}).Validate() != "" {
		t.Error("zero day clears, so it must pass")
	}
	if (&Patch{Kind: new(string)}).Validate() == "" {
		t.Error("empty kind accepted")
	}
}

func TestApplyShared(t *testing.T) {
	now := at(2026, 10, 20)
	hdfcID, regaliaID, otherID := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	mk := func(id bson.ObjectID, name string, limit, spent int64, with *bson.ObjectID) View {
		return Compute(Card{ID: id, Name: name, LimitPaise: limit, SharedWith: with}, spent, 1, nil, now)
	}
	vs := []View{
		mk(hdfcID, "Millennia", 100_000_00, 30_000_00, nil),
		mk(regaliaID, "Regalia", 0, 20_000_00, &hdfcID),
		mk(otherID, "Axis", 50_000_00, 5_000_00, nil),
	}
	ApplyShared(vs)
	for _, i := range []int{0, 1} {
		v := vs[i]
		if v.LimitPaise != 100_000_00 || v.GroupUsedPaise != 50_000_00 || *v.AvailablePaise != 50_000_00 || *v.Utilisation != 0.5 {
			t.Errorf("%s: limit %d group used %d available %d util %v", v.Name, v.LimitPaise, v.GroupUsedPaise, *v.AvailablePaise, *v.Utilisation)
		}
	}
	if vs[1].SharedWithName != "Millennia" || len(vs[1].SharedCards) != 1 || vs[0].SharedWithName != "" || vs[0].SharedCards[0] != "Regalia" {
		t.Errorf("names: %+v / %+v", vs[1].SharedCards, vs[0].SharedCards)
	}
	if vs[0].UsedPaise != 30_000_00 || vs[1].UsedPaise != 20_000_00 {
		t.Error("a card's own used amount must stay its own")
	}
	if vs[2].GroupUsedPaise != 0 || *vs[2].AvailablePaise != 45_000_00 || len(vs[2].SharedCards) != 0 {
		t.Errorf("unshared card changed: %+v", vs[2])
	}

	// an owner that is missing, or that itself shares, leaves the card standalone
	ghost := bson.NewObjectID()
	orphan := []View{mk(regaliaID, "Regalia", 10_000_00, 1_000_00, &ghost)}
	ApplyShared(orphan)
	if orphan[0].SharedWith != nil || orphan[0].LimitPaise != 10_000_00 {
		t.Errorf("orphan: %+v", orphan[0])
	}
	chain := []View{mk(hdfcID, "A", 10_000_00, 0, &regaliaID), mk(regaliaID, "B", 0, 0, &otherID), mk(otherID, "C", 5_000_00, 0, nil)}
	ApplyShared(chain)
	if chain[0].SharedWith != nil {
		t.Error("a card pointing at a sharing card must fall back to standalone")
	}
}
