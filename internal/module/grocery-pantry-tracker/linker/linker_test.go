package linker

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/module/grocery-pantry-tracker/orders"
	"ledger-backend/internal/module/transactions"
)

func TestMatch(t *testing.T) {
	at := time.Date(2026, 11, 3, 19, 30, 0, 0, time.UTC)
	o := orders.Order{PlacedAt: at, TotalPaise: 50000}
	mk := func(paise int64, d time.Duration, dir string) transactions.Transaction {
		return transactions.Transaction{ID: bson.NewObjectID(), AmountPaise: paise, ReceivedAt: at.Add(d), Direction: dir, Type: "expense"}
	}
	far, wrongAmt, credit, near, nearer := mk(50000, 50*time.Minute, "debit"), mk(50200, 0, "debit"), mk(50000, 0, "credit"), mk(50100, 20*time.Minute, "debit"), mk(49950, -5*time.Minute, "debit")
	got := Match(o, []transactions.Transaction{far, wrongAmt, credit, near, nearer}, nil)
	if got == nil || got.ID != nearer.ID {
		t.Fatalf("got %v, want the closest in-window debit", got)
	}
	if Match(o, []transactions.Transaction{nearer}, map[bson.ObjectID]bool{nearer.ID: true}) != nil {
		t.Error("matched a transaction another order already has")
	}
}

func TestParts(t *testing.T) {
	dairy, veg, rest := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
	o := orders.Order{TotalPaise: 10400, Bill: &orders.Bill{ItemPaise: 10000, DeliveryPaise: 300, PackagingPaise: 100},
		Items: []orders.Item{{Name: "Milk", Qty: 2}, {Name: "Onion", Qty: 1}, {Name: "Mystery", Qty: 1}}}
	prices := map[string]int64{"milk": 3000, "onion": 3000} // Mystery is unpriced: counts at the average, 3000
	cat := map[string]bson.ObjectID{"milk": dairy, "onion": veg}
	got := Parts(o, 10400, rest, prices, cat)
	var sum int64
	for _, p := range got {
		sum += p
	}
	// milk is 6000 of 12000 weight: 5000; onion and Mystery 2500 each; Mystery has no category so it and the 400 of fees go to rest
	if sum != 10400 || got[dairy] != 5000 || got[veg] != 2500 || got[rest] != 2900 {
		t.Errorf("parts %v", got)
	}
	// every item already in the payment's own category: nothing to change
	if p := plan(Parts(o, 10400, rest, prices, map[string]bson.ObjectID{"milk": rest, "onion": rest}), rest); p != nil {
		t.Errorf("expected no change, got %+v", p)
	}
	if p := plan(got, rest); p == nil || p.Splits == nil || len(*p.Splits) != 3 {
		t.Errorf("plan %+v", p)
	}
}
