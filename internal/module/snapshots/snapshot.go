// Package snapshots keeps one row per IST day with everything the app otherwise recomputes on each load: every bank account's balance,
// the value of stocks, mutual funds and other holdings, net worth, what is owed, and that day's spending and income. Charts and history
// read these rows instead of replaying transactions.
//
// Today's row is rewritten from live figures every few minutes (Source "live", Valuation "market"). Past days are rebuilt from the
// transactions, which gives exact balances and spending, but prices were never stored, so holdings are valued at cost
// (Valuation "cost") on days that had no live row. A live row keeps its market values once the day is over.
package snapshots

import (
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/stocks"
)

const Collection = "daily_snapshots"

const (
	SourceLive     = "live"
	SourceBackfill = "backfill"
	ValuedMarket   = "market" // holdings at their quoted price or NAV on the day
	ValuedCost     = "cost"   // holdings at what was paid: no price was recorded for that day
)

type AccountBal struct {
	ID    bson.ObjectID `bson:"id" json:"id"`
	Name  string        `bson:"name" json:"name"`
	Bank  string        `bson:"bank" json:"bank"`
	Last4 string        `bson:"last4" json:"last4"`
	Paise int64         `bson:"paise" json:"paise"`
}

// Holding is what was paid for a group of investments and what it was worth. Paise.
type Holding struct {
	Invested int64 `bson:"invested" json:"investedPaise"`
	Value    int64 `bson:"value" json:"valuePaise"`
}

// Snapshot is one day. All money is paise. Pointers are absent when the figure cannot be known for that day.
type Snapshot struct {
	Date string `bson:"_id" json:"date"` // YYYY-MM-DD, IST

	Accounts []AccountBal `bson:"accounts" json:"accounts"`
	Cash     int64        `bson:"cash" json:"cashPaise"` // all accounts together at the end of the day

	Stocks Holding `bson:"stocks" json:"stocks"`
	MF     Holding `bson:"mf" json:"mutualFunds"`
	Other  Holding `bson:"other" json:"other"`
	// Invested is the cost of all investments, Investments their value. NetWorth = Cash + Investments; Owed is beside it, never subtracted.
	Invested    int64  `bson:"invested" json:"investedPaise"`
	Investments int64  `bson:"investments" json:"investmentsPaise"`
	NetWorth    int64  `bson:"net_worth" json:"netWorthPaise"`
	Owed        *int64 `bson:"owed,omitempty" json:"owedPaise,omitempty"`           // EMI principal still owed
	CardUsed    *int64 `bson:"card_used,omitempty" json:"cardUsedPaise,omitempty"`  // credit used across cards; live rows only

	// The day's own movement.
	Spent        int64 `bson:"spent" json:"spentPaise"`        // the user's spending, as the dashboard counts it
	SpentMonth   int64 `bson:"spent_month" json:"spentMonthPaise"` // spending from the 1st to this day
	SpendTxns    int   `bson:"spend_txns" json:"spendTxns"`
	Income       int64 `bson:"income" json:"incomePaise"`
	CardPayments int64 `bson:"card_payments" json:"cardPaymentsPaise"` // paid toward card bills; already inside Spent's purchases

	Valuation string    `bson:"valuation" json:"valuation"`
	Source    string    `bson:"source" json:"source"`
	UpdatedAt time.Time `bson:"updated_at" json:"updatedAt"`
}

// Seal fills the totals that follow from the parts. Call after setting Accounts and the three holdings.
func (s *Snapshot) Seal() {
	s.Cash, s.Invested, s.Investments = 0, 0, 0
	for _, a := range s.Accounts {
		s.Cash += a.Paise
	}
	for _, h := range []Holding{s.Stocks, s.MF, s.Other} {
		s.Invested += h.Invested
		s.Investments += h.Value
	}
	s.NetWorth = s.Cash + s.Investments
}

func toPaise(rupees float64) int64 { return int64(math.Round(rupees * 100)) }

// Day formats t as its IST date.
func Day(t time.Time) string { return t.In(ist.Loc).Format("2006-01-02") }

// DaysBetween lists the IST dates from..to inclusive; empty when to is before from.
func DaysBetween(from, to string) []string {
	a, err1 := time.ParseInLocation("2006-01-02", from, ist.Loc)
	b, err2 := time.ParseInLocation("2006-01-02", to, ist.Loc)
	if err1 != nil || err2 != nil {
		return nil
	}
	var out []string
	for d := a; !d.After(b); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out
}

// Balances walks the days in order and returns each account's end-of-day balance: opening balance plus the net movement up to and including
// that day. deltas maps account -> day -> net movement (only transactions on or after the account's opening). An account does not appear
// on days before its opening day. Pure.
func Balances(days []string, accs []accounts.Account, deltas map[bson.ObjectID]map[string]int64) map[string][]AccountBal {
	out := make(map[string][]AccountBal, len(days))
	if len(days) == 0 {
		return out
	}
	for _, a := range accs {
		run := a.OpeningBalancePaise
		for d, v := range deltas[a.ID] { // movement before the first listed day
			if d < days[0] {
				run += v
			}
		}
		opened := Day(a.OpeningAt)
		for _, d := range days {
			run += deltas[a.ID][d]
			if d < opened {
				continue
			}
			out[d] = append(out[d], AccountBal{ID: a.ID, Name: a.Name, Bank: a.Bank, Last4: a.Last4, Paise: run})
		}
	}
	return out
}

// StocksAt is the cost of the lots bought on or before day. Pure.
func StocksAt(lots []stocks.Lot, day string) Holding {
	var h float64
	for _, l := range lots {
		if l.BuyDate <= day {
			h += l.Quantity * l.BuyPrice
		}
	}
	return Holding{Invested: toPaise(h), Value: toPaise(h)}
}

// FundsAt is the cost of the funds held on day: joined on or before it (no join date counts as always held) and not yet exited. Pure.
func FundsAt(fs []funds.Fund, day string) Holding {
	var h float64
	for _, f := range fs {
		switch {
		case f.JoinedAt != "" && f.JoinedAt > day:
		case f.Exited() && (f.ExitedAt == "" || f.ExitedAt <= day):
		default:
			h += f.Invested
		}
	}
	return Holding{Invested: toPaise(h), Value: toPaise(h)}
}
