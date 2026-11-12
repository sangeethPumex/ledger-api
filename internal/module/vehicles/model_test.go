package vehicles

import (
	"testing"
	"time"

	"ledger-backend/internal/ist"
)

var now = time.Date(2026, 11, 3, 12, 0, 0, 0, ist.Loc)

func TestMileage(t *testing.T) {
	logs := []Log{
		{Kind: "fuel", Date: "2026-09-01", OdometerKm: 10_000, Litres: 30, FullTank: true, AmountPaise: 300_000},
		{Kind: "fuel", Date: "2026-09-10", OdometerKm: 10_200, Litres: 10, AmountPaise: 100_000}, // partial: counts toward the next full tank
		{Kind: "fuel", Date: "2026-09-20", OdometerKm: 10_600, Litres: 30, FullTank: true, AmountPaise: 315_000},
		{Kind: "fuel", Date: "2026-10-01", Litres: 5, AmountPaise: 50_000}, // no odometer: breaks the chain
		{Kind: "fuel", Date: "2026-10-05", OdometerKm: 11_000, Litres: 20, FullTank: true, AmountPaise: 200_000},
		{Kind: "fuel", Date: "2026-10-25", OdometerKm: 11_500, Litres: 25, FullTank: true, AmountPaise: 260_000},
	}
	f := mileage(logs)
	if len(f) != 2 || f[0].Km != 600 || f[0].Litres != 40 || f[0].Kmpl != 15 || f[1].Kmpl != 20 || f[1].PricePerL != 104 {
		t.Fatalf("fills = %+v", f)
	}
	s := Build(Vehicle{TankLitres: 40, Insurance: Insurance{Expires: "2026-11-10", PremiumPaise: 1_200_000}, PUCExpires: "2026-10-30",
		ServiceKm: 10_000, OdometerKm: 9_000, CreatedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, ist.Loc)}, logs, now)
	// 1100 km over 65 l, across the two measured tanks
	if s.AvgKmpl != 16.92 || s.OdometerKm != 11_500 || s.PricePerL != 104 || s.RangeKm != 676 {
		t.Fatalf("avg %v odo %v price %v range %v", s.AvgKmpl, s.OdometerKm, s.PricePerL, s.RangeKm)
	}
	if s.FuelPerKmPaise != 615 { // ₹104/l over 16.92 km/l
		t.Fatalf("fuel per km = %d", s.FuelPerKmPaise)
	}
	if s.NextService == nil || s.NextService.DueKm != 20_000 || s.NextService.KmLeft != 8_500 { // no service logged: the next 10,000 on the clock
		t.Fatalf("service = %+v", s.NextService)
	}
	if len(s.Reminders) != 3 || s.Reminders[0].Kind != "puc" || s.Reminders[0].Tone != "bad" || s.Reminders[1].Kind != "insurance" || s.Reminders[1].Days != 7 {
		t.Fatalf("reminders = %+v", s.Reminders)
	}
	if len(s.Months) != 12 || s.Months[11].Month != "2026-11" || s.Months[10].FuelPaise != 510_000 || s.TotalPaise != 1_225_000 {
		t.Fatalf("months = %+v total %d", s.Months, s.TotalPaise)
	}
}

func TestMileageEstimate(t *testing.T) {
	logs := []Log{
		{Kind: "fuel", Date: "2026-10-01", OdometerKm: 6_000, Litres: 20, AmountPaise: 200_000},
		{Kind: "fuel", Date: "2026-10-10", OdometerKm: 131, Litres: 30, AmountPaise: 300_000}, // trip km typed as the odometer: skipped, litres still count
		{Kind: "fuel", Date: "2026-10-20", OdometerKm: 6_800, Litres: 25, AmountPaise: 250_000},
	}
	s := Build(Vehicle{TankLitres: 40}, logs, now)
	if !s.Estimated || s.AvgKmpl != 14.55 || len(s.Fills) != 0 { // 800 km over the 55 l bought after the first fill
		t.Fatalf("estimate = %+v", s)
	}
	if s := Build(Vehicle{}, logs[:1], now); s.AvgKmpl != 0 || s.Estimated {
		t.Fatalf("one reading cannot measure a distance: %+v", s)
	}
}

func TestServiceSchedule(t *testing.T) {
	v := Vehicle{ServiceMonths: 12, BoughtOn: "2026-02-10", CreatedAt: now}
	if ns := Build(v, nil, now).NextService; ns == nil || ns.Suggested != "2027-02-10" || ns.DueDate != "2027-02-10" {
		t.Fatalf("from purchase: %+v", ns)
	}
	v.ServicePlanned = "2027-01-20"
	if ns := Build(v, nil, now).NextService; ns.DueDate != "2027-01-20" || ns.Suggested != "2027-02-10" {
		t.Fatalf("planned: %+v", ns)
	}
	// Once serviced (after the booked date), the cycle runs from that service and the old booking is ignored.
	later := time.Date(2027, 3, 1, 12, 0, 0, 0, ist.Loc)
	if ns := Build(v, []Log{{Kind: "service", Date: "2027-02-12", AmountPaise: 1}}, later).NextService; ns.DueDate != "2028-02-12" || ns.Planned != "" {
		t.Fatalf("after service: %+v", ns)
	}
	if _, msg := (VehicleInput{Kind: "car", Name: "x", Plate: "x", Fuel: "petrol", Insurance: Insurance{Type: "zero_dep"}}).Parse(); msg == "" {
		t.Error("unknown insurance type accepted")
	}
}

func TestStage(t *testing.T) {
	for days, want := range map[int]int{-3: 0, 0: 0, 1: 1, 2: 7, 7: 7, 8: 30, 30: 30} {
		if got, ok := stage(days); !ok || got != want {
			t.Errorf("stage(%d) = %d, %v", days, got, ok)
		}
	}
	if _, ok := stage(31); ok {
		t.Error("31 days out should not push yet")
	}
}

func TestParse(t *testing.T) {
	v, msg := VehicleInput{Kind: "car", Name: " City ", Plate: "kl 07   ab 1234", Fuel: "petrol"}.Parse()
	if msg != "" || v.Plate != "KL 07 AB 1234" || v.Name != "City" {
		t.Fatalf("%q %+v", msg, v)
	}
	if _, msg := (VehicleInput{Kind: "truck", Name: "x", Plate: "x", Fuel: "petrol"}).Parse(); msg == "" {
		t.Error("truck accepted")
	}
	if _, msg := (LogInput{Kind: "fuel", Date: "2026-11-01", AmountPaise: 100}).Parse(); msg == "" {
		t.Error("fuel without litres accepted")
	}
	if l, msg := (LogInput{Kind: "trip", Date: "2026-11-01", Place: "Munnar", TripKm: 120}).Parse(); msg != "" || l.Place != "Munnar" {
		t.Errorf("trip: %q", msg)
	}
}
