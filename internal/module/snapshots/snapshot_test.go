package snapshots

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/stocks"
)

func TestDaysBetween(t *testing.T) {
	got := DaysBetween("2026-10-30", "2026-11-02")
	if len(got) != 4 || got[0] != "2026-10-30" || got[3] != "2026-11-02" {
		t.Errorf("%v", got)
	}
	if len(DaysBetween("2026-11-02", "2026-10-30")) != 0 || DaysBetween("nope", "2026-10-30") != nil {
		t.Error("bad range must be empty")
	}
}

func TestBalances(t *testing.T) {
	a, b := bson.NewObjectID(), bson.NewObjectID()
	accs := []accounts.Account{
		{ID: a, Name: "Federal", OpeningBalancePaise: 100000, OpeningAt: time.Date(2026, 10, 1, 0, 0, 0, 0, ist.Loc)},
		{ID: b, Name: "HDFC", OpeningBalancePaise: 5000, OpeningAt: time.Date(2026, 10, 3, 9, 0, 0, 0, ist.Loc)},
	}
	deltas := map[bson.ObjectID]map[string]int64{
		a: {"2026-09-30": 7, "2026-10-01": -2000, "2026-10-03": 50000}, // 09-30 is before the range: carried into the first day
		b: {"2026-10-03": -1000},
	}
	got := Balances([]string{"2026-10-01", "2026-10-02", "2026-10-03"}, accs, deltas)
	if len(got["2026-10-01"]) != 1 || got["2026-10-01"][0].Paise != 100000+7-2000 {
		t.Errorf("day 1: %+v", got["2026-10-01"])
	}
	if got["2026-10-02"][0].Paise != 98007 { // a quiet day keeps the balance
		t.Errorf("day 2: %+v", got["2026-10-02"])
	}
	d3 := got["2026-10-03"]
	if len(d3) != 2 || d3[0].Paise != 148007 || d3[1].Paise != 4000 { // b appears from its opening day
		t.Errorf("day 3: %+v", d3)
	}
}

func TestHoldingsAt(t *testing.T) {
	lots := []stocks.Lot{{Quantity: 10, BuyPrice: 100, BuyDate: "2026-10-05"}, {Quantity: 1, BuyPrice: 50.5, BuyDate: "2026-10-20"}}
	if h := StocksAt(lots, "2026-10-04"); h.Invested != 0 {
		t.Errorf("before any buy: %+v", h)
	}
	if h := StocksAt(lots, "2026-10-20"); h.Invested != 105050 || h.Value != h.Invested {
		t.Errorf("both lots: %+v", h)
	}

	fs := []funds.Fund{
		{Invested: 1000},                                       // no join date: always held
		{Invested: 500, JoinedAt: "2026-10-10"},                // joins later
		{Invested: 200, Status: "exited", ExitedAt: "2026-10-15"}, // gone from the 15th
	}
	if h := FundsAt(fs, "2026-10-09"); h.Invested != 120000 { // 1000 + 200
		t.Errorf("9th: %+v", h)
	}
	if h := FundsAt(fs, "2026-10-15"); h.Invested != 150000 { // 1000 + 500
		t.Errorf("15th: %+v", h)
	}
}

func TestSeal(t *testing.T) {
	s := Snapshot{Accounts: []AccountBal{{Paise: 100}, {Paise: 50}}, Stocks: Holding{10, 12}, MF: Holding{20, 25}, Other: Holding{1, 1}}
	s.Seal()
	if s.Cash != 150 || s.Invested != 31 || s.Investments != 38 || s.NetWorth != 188 {
		t.Errorf("%+v", s)
	}
}
