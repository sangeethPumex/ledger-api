package transactions

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

var editNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func okEdit() EditPatch {
	at := editNow.Add(-time.Hour)
	return EditPatch{Type: "expense", AmountPaise: 25_000, OccurredAt: &at}
}

func TestPlanEdit(t *testing.T) {
	card := bson.NewObjectID()
	for _, c := range []struct {
		name string
		t    Transaction
		edit func(*EditPatch)
		bad  string // empty means the edit is fine
	}{
		{"plain", Transaction{AmountPaise: 100, Direction: "debit"}, func(e *EditPatch) {}, ""},
		{"zero amount", Transaction{}, func(e *EditPatch) { e.AmountPaise = 0 }, "amount"},
		{"too big", Transaction{}, func(e *EditPatch) { e.AmountPaise = maxPaise + 1 }, "amount"},
		{"future", Transaction{}, func(e *EditPatch) { at := editNow.Add(48 * time.Hour); e.OccurredAt = &at }, "future"},
		{"no date", Transaction{}, func(e *EditPatch) { e.OccurredAt = &time.Time{} }, "date"},
		{"bad type", Transaction{}, func(e *EditPatch) { e.Type = "gift" }, "type"},
		{"transfer needs a way", Transaction{}, func(e *EditPatch) { e.Type = "transfer" }, "out or came in"},
		{"transfer in", Transaction{}, func(e *EditPatch) { e.Type, e.Direction = "transfer", "credit" }, ""},
		{"long note", Transaction{}, func(e *EditPatch) { e.Note = strings.Repeat("x", 301) }, "too long"},
		{"card payment stays one", Transaction{PaidCardID: &card, Type: "card_bill_payment", Direction: "debit"}, func(e *EditPatch) {}, "unlink"},
		{"card payment kept", Transaction{PaidCardID: &card, Type: "card_bill_payment", Direction: "debit"}, func(e *EditPatch) { e.Type, e.Direction = "card_bill_payment", "debit" }, ""},
		{"split blocks amount", Transaction{AmountPaise: 100, Splits: []Split{{}, {}}}, func(e *EditPatch) {}, "split"},
		{"split with same amount", Transaction{AmountPaise: 25_000, Splits: []Split{{}, {}}}, func(e *EditPatch) {}, ""},
		{"owed blocks credit", Transaction{AmountPaise: 25_000, OwedPaise: 100}, func(e *EditPatch) { e.Type = "income" }, "owed"},
		{"owed above new amount", Transaction{AmountPaise: 90_000, OwedPaise: 30_000}, func(e *EditPatch) {}, "owed"},
		{"owed within amount", Transaction{AmountPaise: 90_000, OwedPaise: 25_000}, func(e *EditPatch) {}, ""},
	} {
		e := okEdit()
		c.edit(&e)
		set, unset, err := planEdit(c.t, &e, editNow)
		if c.bad == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			} else if set["amount_paise"] != e.AmountPaise || set["received_at"] != set["occurred_at"] {
				t.Errorf("%s: set = %v (unset %v)", c.name, set, unset)
			}
			continue
		}
		var bad ErrPatch
		if err == nil || !strings.Contains(err.Error(), c.bad) {
			t.Errorf("%s: err = %v, want one with %q", c.name, err, c.bad)
		} else if bad = err.(ErrPatch); bad == "" {
			t.Errorf("%s: empty message", c.name)
		}
	}
}

func TestPlanEditLeavesTheDateAloneWhenNotSent(t *testing.T) {
	e := okEdit()
	e.OccurredAt = nil
	set, _, err := planEdit(Transaction{}, &e, editNow)
	if _, has := set["occurred_at"]; err != nil || has {
		t.Fatalf("set = %v, err = %v", set, err)
	}
}

func TestPlanEditDirectionAndText(t *testing.T) {
	e := okEdit()
	e.Type, e.Direction, e.Note, e.Ref = "income", "debit", "  rent back  ", "  "
	set, unset, err := planEdit(Transaction{}, &e, editNow)
	if err != nil || set["direction"] != "credit" || set["note"] != "rent back" {
		t.Fatalf("set = %v, err = %v", set, err)
	}
	if _, ok := unset["ref"]; !ok {
		t.Fatalf("an emptied ref should be unset: %v", unset)
	}
}
