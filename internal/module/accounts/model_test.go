package accounts

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
)

func TestCleanBank(t *testing.T) {
	for in, want := range map[string]string{
		"Federal Bank \u2022\u20222411": "Federal Bank",
		"Kotak Bank \u20229959":         "Kotak Bank",
		"Federal Bank \ufffd\ufffd2411": "Federal Bank", // a bullet mangled in transit
		"HDFC Bank - 5963":              "HDFC Bank",
		"  Union Bank of India  ":       "Union Bank of India",
		"HDFC Bank":                     "HDFC Bank",
		"\u2022\u20222411":              "\u2022\u20222411", // nothing left, so keep it
	} {
		if got := CleanBank(in); got != want {
			t.Errorf("CleanBank(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccountDetailFigures(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, ist.Loc)
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, ist.Loc) }
	food := bson.NewObjectID()
	flows := []flow{
		{Type: "income", Direction: "credit", AmountPaise: 8500_00, ReceivedAt: at(26, 10)},
		{Type: "expense", Direction: "debit", AmountPaise: 300_00, ReceivedAt: at(27, 12), RawVendor: "Swiggy", CategoryID: &food},
		{Type: "expense", Direction: "debit", AmountPaise: 200_00, ReceivedAt: at(28, 9), RawVendor: "swiggy", CategoryID: &food},
		{Type: "card_bill_payment", AmountPaise: 1000_00, ReceivedAt: at(28, 11)},           // no direction: falls back to type, and is not spending
		{Type: "transfer", Direction: "credit", AmountPaise: 50_00, OccurredAt: at(28, 12)}, // no arrival time: uses the SMS date
	}
	var net int64
	for _, f := range flows {
		net += f.signedPaise()
	}
	if net != 8500_00-300_00-200_00-1000_00+50_00 {
		t.Fatalf("net %d", net)
	}

	in, out, n := MonthFlows(flows, now)
	if in != 8550_00 || out != 1500_00 || n != 5 {
		t.Errorf("month: in %d out %d n %d", in, out, n)
	}

	series := Series(1000_00, flows, now)
	if len(series) != 30 || series[29].Paise != 1000_00 || series[29].Date != "2026-10-28" {
		t.Fatalf("series end: %+v", series[29])
	}
	// before the 26th's income landed the balance was lower by that income, and by the later movements
	if got, want := series[27].Paise, 1000_00-(net-8500_00); got != want || series[27].Date != "2026-10-26" && series[26].Date != "2026-10-26" {
		t.Errorf("series on 27th: %d want %d", got, want)
	}
	if series[0].Paise != 1000_00-net {
		t.Errorf("series start %d, want %d", series[0].Paise, 1000_00-net)
	}

	since := at(1, 0)
	p := TopPayees(flows, since, 5)
	if len(p) != 1 || p[0].Name != "Swiggy" || p[0].Paise != 500_00 || p[0].Count != 2 {
		t.Errorf("payees (names merge case-insensitively, bill payments are not spend): %+v", p)
	}
	c := SpendByCategory(flows, since)
	if len(c) != 1 || *c[0].CategoryID != food || c[0].Paise != 500_00 {
		t.Errorf("categories: %+v", c)
	}
	if len(TopPayees(flows, at(28, 0), 5)) != 1 || TopPayees(flows, at(28, 0), 5)[0].Paise != 200_00 {
		t.Error("since must cut off earlier spends")
	}
}
