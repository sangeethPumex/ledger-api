package transactions

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestPlanPatch(t *testing.T) {
	cp, other, card := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	debit := Transaction{Type: "expense", Direction: "debit"}

	if _, _, err := planPatch(debit, cp, &other, &card, false); err == nil {
		t.Error("a card needs the card payments category")
	}
	if _, _, err := planPatch(Transaction{Direction: "credit"}, cp, &cp, &card, false); err == nil {
		t.Error("money coming in cannot pay a card")
	}

	set, _, err := planPatch(debit, cp, &cp, &card, false)
	if err != nil || set["paid_card_id"] != card || set["type"] != "card_bill_payment" || set["type_before"] != "expense" || set["category_set"] != true {
		t.Fatalf("link: %v %v", set, err)
	}

	linked := debit
	linked.Type, linked.TypeBefore, linked.CategoryID, linked.PaidCardID = "card_bill_payment", "expense", &cp, &card
	set, unset, _ := planPatch(linked, cp, &other, nil, false) // moved to another category: unlinked, type restored
	if _, ok := unset["paid_card_id"]; !ok || set["type"] != "expense" {
		t.Errorf("category change: set %v unset %v", set, unset)
	}
	set, unset, _ = planPatch(linked, cp, nil, nil, true) // explicit clear keeps the category
	if _, ok := unset["paid_card_id"]; !ok || set["type"] != "expense" || set["category_id"] != nil {
		t.Errorf("clear: set %v unset %v", set, unset)
	}
	next := bson.NewObjectID()
	if set, _, _ = planPatch(linked, cp, nil, &next, false); set["paid_card_id"] != next || set["type"] != nil {
		t.Errorf("switch card: %v", set)
	}
}

func TestPlanOwed(t *testing.T) {
	debit := Transaction{Direction: "debit", AmountPaise: 1000}
	if _, _, err := planOwed(Transaction{Direction: "credit", AmountPaise: 1000}, &OwedPatch{From: "Ravi", Paise: 500}, nil); err == nil {
		t.Error("money in cannot be owed back")
	}
	for _, bad := range []OwedPatch{{From: "Ravi", Paise: 1001}, {From: "Ravi", Paise: -5}, {From: " ", Paise: 500}} {
		if _, _, err := planOwed(debit, &bad, nil); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	set, _, err := planOwed(debit, &OwedPatch{From: " Ravi ", Paise: 600}, nil)
	if err != nil || set["owed_from"] != "Ravi" || set["owed_paise"] != int64(600) || set["repaid_paise"] != int64(0) {
		t.Fatalf("mark owed: %v %v", set, err)
	}

	owed := debit
	owed.OwedFrom, owed.OwedPaise, owed.RepaidPaise = "Ravi", 600, 400
	over := int64(700)
	if _, _, err := planOwed(owed, nil, &over); err == nil {
		t.Error("repaid above owed accepted")
	}
	if _, _, err := planOwed(debit, nil, &over); err == nil {
		t.Error("repayment on something not owed accepted")
	}
	if set, _, _ := planOwed(owed, &OwedPatch{From: "Ravi", Paise: 300}, nil); set["repaid_paise"] != int64(300) {
		t.Errorf("repaid not clamped to the smaller owed amount: %v", set)
	}
	full := int64(600)
	if set, _, err := planOwed(owed, nil, &full); err != nil || set["repaid_paise"] != full {
		t.Errorf("settle: %v %v", set, err)
	}
	if _, unset, _ := planOwed(owed, &OwedPatch{}, nil); len(unset) != 3 {
		t.Errorf("clear: %v", unset)
	}
}

func TestPlanSplits(t *testing.T) {
	a, b := bson.NewObjectID(), bson.NewObjectID()
	pay := Transaction{AmountPaise: 1000, Direction: "debit"}
	parts := func(p ...int64) []Split {
		out := []Split{}
		for i, x := range p {
			out = append(out, Split{CategoryID: []bson.ObjectID{a, b}[i%2], Paise: x})
		}
		return out
	}
	owed, credit := pay, pay
	owed.OwedPaise = 100
	credit.Direction = "credit"
	for name, c := range map[string]struct {
		t     Transaction
		parts []Split
		bad   bool
	}{
		"clears with no parts": {pay, nil, false},
		"adds up":              {pay, parts(600, 400), false},
		"short":                {pay, parts(600, 300), true},
		"one part":             {pay, parts(1000), true},
		"zero part":            {pay, parts(1000, 0), true},
		"owed":                 {owed, parts(600, 400), true},
		"credit":               {credit, parts(600, 400), true},
	} {
		if err := planSplits(c.t, c.parts); (err != nil) != c.bad {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
