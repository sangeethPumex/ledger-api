package basket

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ledger-backend/internal/module/grocery-pantry-tracker/orders"
)

var now = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

func im(daysAgo int, fee int64, itemPaise int64, items ...orders.Item) orders.Order {
	return orders.Order{Source: orders.SourceInstamart, OrderID: time.Duration(daysAgo).String(), PlacedAt: now.AddDate(0, 0, -daysAgo), Status: "DELIVERED", Items: items,
		Bill: &orders.Bill{ItemPaise: itemPaise, DeliveryPaise: fee}}
}

func TestBuildAndLow(t *testing.T) {
	milk := orders.Item{Name: "Amul Taaza Toned Milk 500ml", Qty: 2}
	all := []orders.Order{im(30, 0, 50000, milk), im(23, 1800, 20000, milk), im(16, 1800, 20000, milk), im(9, 0, 50000, orders.Item{Name: "Salt", Qty: 1})}
	items := Build(all, now)
	by := map[string]Item{}
	for _, it := range items {
		by[it.Key] = it
	}
	m := by["amul taaza toned milk 500ml"]
	if m.Buys != 3 || m.Qty != 2 || m.IntervalDays != 7 {
		t.Fatalf("milk %+v", m)
	}
	if !m.Low(now) { // 16 days since, usual gap 7
		t.Error("milk should be low")
	}
	if salt := by["salt"]; salt.Low(now) || salt.IntervalDays != 0 {
		t.Errorf("an item bought once has no interval to go on: %+v", salt)
	}
	fresh := m
	fresh.LastBought = now.AddDate(0, 0, -2)
	if fresh.Low(now) {
		t.Error("milk bought two days ago is not low")
	}
}

func TestSimilarity(t *testing.T) {
	if s := similarity("Amul Taaza Toned Milk 500ml", "Amul Taaza Toned Milk 500 ml"); s < 0.5 {
		t.Errorf("close names scored %.2f", s)
	}
	if s := similarity("Amul Taaza Toned Milk 500ml", "Britannia Bread"); s != 0 {
		t.Errorf("unrelated names scored %.2f", s)
	}
}

func TestSuggestFeesAndTipper(t *testing.T) {
	past := []orders.Order{im(40, 0, 30000), im(10, 1800, 12000), im(5, 1800, 9000)}
	low := func(key string, price int64) Item {
		return Item{Key: key, Name: key, Qty: 1, Buys: 3, IntervalDays: 7, LastBought: now.AddDate(0, 0, -14), SpinID: "s" + key, SkuID: "k" + key, PricePaise: price, Available: true}
	}
	items := []Item{low("milk", 25000), low("eggs", 1000), {Key: "nosku", Name: "x", Buys: 3, IntervalDays: 7, LastBought: now.AddDate(0, 0, -14)}}
	items[1].LastBought = now // not low: it can only be the tipper
	p := Suggest(items, past, now)
	if len(p.Lines) != 1 || p.TotalPaise != 25000 || p.NoSku != 1 {
		t.Fatalf("basket %+v", p)
	}
	if p.ThresholdPaise != 30000 || p.DeliveryFree {
		t.Errorf("threshold %d free %v", p.ThresholdPaise, p.DeliveryFree)
	}
	if p.FeesPaise != 3600 || p.FeeOrders != 2 {
		t.Errorf("fee leak %d over %d", p.FeesPaise, p.FeeOrders)
	}
	if p.Tipper != nil { // eggs cost 10 and the gap is 50: nothing covers it, so nothing is pushed
		t.Errorf("tipper %+v", p.Tipper)
	}
	items[1].PricePaise = 6000
	if p = Suggest(items, past, now); p.Tipper == nil || p.Tipper.Key != "eggs" {
		t.Errorf("tipper %+v", p.Tipper)
	}
}

type fake struct{ calls []string }

func (f *fake) Call(_ context.Context, _, tool string, args map[string]any, out any) error {
	f.calls = append(f.calls, tool)
	if tool == "get_cart" {
		return json.Unmarshal([]byte(`{"items":[{"spinId":"sA","skuId":"kA","quantity":3}],"selectedAddressDetails":{"id":"addr1"},"cartTotalAmount":"90"}`), out)
	}
	items, _ := args["items"].([]map[string]any)
	f.calls = append(f.calls, "n="+string(rune('0'+len(items))))
	return json.Unmarshal([]byte(`{"cartTotalAmount":"250"}`), out)
}

// update_cart replaces the cart, so the item already in it must be sent back along with the new one.
func TestAddKeepsCartContents(t *testing.T) {
	f := &fake{}
	s := &Service{swiggy: f}
	cart, err := s.cartWith(context.Background(), []wanted{{"sA", "kA", 1}, {"sB", "kB", 2}})
	if err != nil {
		t.Fatal(err)
	}
	if cart.Added != 1 || cart.InCart != 1 || cart.CartTotal != "250" || f.calls[len(f.calls)-1] != "n=2" {
		t.Errorf("%+v %v", cart, f.calls)
	}
}
