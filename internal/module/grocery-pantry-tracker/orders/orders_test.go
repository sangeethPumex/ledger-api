package orders

import (
	"encoding/json"
	"testing"
	"time"

	"ledger-backend/internal/ist"
)

func TestPaise(t *testing.T) {
	for in, want := range map[flex]int64{"₹1,234.50": 123450, "349": 34900, "99.999": 10000, "Rs. 0": 0} {
		if got, ok := paise(in); !ok || got != want {
			t.Errorf("paise(%q) = %d %v, want %d", in, got, ok, want)
		}
	}
	if _, ok := paise("free"); ok {
		t.Error("read a number out of text with none")
	}
}

func TestWhen(t *testing.T) {
	want := time.Date(2026, 11, 3, 19, 30, 0, 0, ist.Loc)
	for _, in := range []string{"2026-11-03T14:00:00Z", "2026-11-03 19:30:00", "2026-11-03T19:30:00", "1793714400", "1793714400000"} {
		if got, ok := when(in); !ok || !got.Equal(want) {
			t.Errorf("when(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	if _, ok := when("yesterday"); ok {
		t.Error("parsed nonsense")
	}
}

func TestWhenYearless(t *testing.T) {
	now := time.Date(2026, 11, 12, 15, 0, 0, 0, ist.Loc)
	for in, want := range map[string]time.Time{
		"October 10, 8:26 PM":  time.Date(2026, 10, 10, 20, 26, 0, 0, ist.Loc),
		"November 9, 9:51 AM":  time.Date(2026, 11, 9, 9, 51, 0, 0, ist.Loc),
		"December 28, 7:59 PM": time.Date(2025, 12, 28, 19, 59, 0, 0, ist.Loc), // not yet happened this year: last year's
	} {
		if got, ok := whenAt(in, now); !ok || !got.Equal(want) {
			t.Errorf("whenAt(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
}

func TestFoodItems(t *testing.T) {
	got := foodItems("Kuboos (4),Masala Shawai Chicken (1)")
	if len(got) != 2 || got[0] != (Item{"Kuboos", 4}) || got[1] != (Item{"Masala Shawai Chicken", 1}) {
		t.Errorf("got %+v", got)
	}
	got = foodItems("Rice, Chicken Bowl (2), Coke (1)") // a comma inside a name stays in it
	if len(got) != 2 || got[0] != (Item{"Rice, Chicken Bowl", 2}) {
		t.Errorf("comma in name: %+v", got)
	}
	if got = foodItems("Mystery combo"); len(got) != 1 || got[0] != (Item{"Mystery combo", 1}) || foodItems("") != nil {
		t.Errorf("unshaped: %+v", got)
	}
}

func TestDecodeAndTotals(t *testing.T) {
	var im struct{ Orders []imOrder }
	var food struct{ Orders []foodOrder }
	// Numbers where the docs say strings and the other way round both have to read.
	_ = json.Unmarshal([]byte(`{"orders":[{"orderId":123,"historyStatus":"DELIVERED","createdAt":"2026-11-03 10:00:00","totalAmount":250.5,"items":[{"name":"Milk","quantity":2}],"paymentMethod":"UPIIntent","billDetails":{"itemTotal":240,"deliveryFee":10,"packagingFee":10.5,"grandTotal":260}},
		{"orderId":"124","status":"CANCELLED","createdAt":"2026-11-04 10:00:00","totalAmount":100}]}`), &im)
	_ = json.Unmarshal([]byte(`{"orders":[{"orderId":"F1","restaurantName":"Dosa Co","orderTotal":"₹180","orderStatus":"Delivered","orderedItems":"Masala dosa x1","orderedTime":"2026-11-05 13:00:00"}]}`), &food)
	var all []Order
	for _, o := range im.Orders {
		all = append(all, fromInstamart(o))
	}
	for _, o := range food.Orders {
		all = append(all, fromFood(o))
	}
	if all[0].OrderID != "123" || all[0].TotalPaise != 26000 || all[0].ItemCount != 1 || all[0].Summary != "Milk" {
		t.Errorf("instamart order %+v", all[0])
	}
	if b := all[0].Bill; b == nil || *b != (Bill{24000, 1000, 1050}) || all[0].Payment != "UPIIntent" {
		t.Errorf("bill %+v payment %q", all[0].Bill, all[0].Payment)
	}
	by, total := Totals(all)
	if by[SourceInstamart] != (Spend{26000, 1}) || by[SourceFood] != (Spend{18000, 1}) || total != 44000 {
		t.Errorf("totals %v %d (the cancelled order must not count)", by, total)
	}
}
