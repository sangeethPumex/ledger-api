// Package portfolio reads funds, stocks and bank balances together: asset allocation against a target mix, XIRR,
// SIP health and the dividend/interest income log. It owns no holdings; those stay in funds and stocks.
package portfolio

import (
	"math"
	"sort"
	"time"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/stocks"
	"ledger-backend/internal/xirr"
)

var classes = [...]string{"equity", "debt", "gold", "cash"}

// Target is the wanted mix in percent; the four add up to 100.
type Target struct {
	Equity float64 `json:"equity" bson:"equity"`
	Debt   float64 `json:"debt" bson:"debt"`
	Gold   float64 `json:"gold" bson:"gold"`
	Cash   float64 `json:"cash" bson:"cash"`
}

func (t Target) of(class string) float64 {
	return map[string]float64{"equity": t.Equity, "debt": t.Debt, "gold": t.Gold, "cash": t.Cash}[class]
}

func (t Target) Validate() string {
	for _, v := range []float64{t.Equity, t.Debt, t.Gold, t.Cash} {
		if !(v >= 0 && v <= 100) { // also rejects NaN
			return "each share must be between 0 and 100"
		}
	}
	if math.Abs(t.Equity+t.Debt+t.Gold+t.Cash-100) > 0.01 {
		return "the target must add up to 100"
	}
	return ""
}

// classOf maps a fund's type to an asset class. Foreign funds hold overseas shares, so they count as equity.
func classOf(fundType string) string {
	switch fundType {
	case "Debt":
		return "debt"
	case "Metal":
		return "gold"
	}
	return "equity"
}

type Slice struct {
	Class string  `json:"class"`
	Value float64 `json:"value"`
	Pct   float64 `json:"pct"`
	// Target, Drift (points, actual minus target) and Shift (rupees to add, or to take out when negative, to reach the target)
	// are set only when a target is.
	Target *float64 `json:"target,omitempty"`
	Drift  *float64 `json:"drift,omitempty"`
	Shift  *float64 `json:"shift,omitempty"`
}

type Allocation struct {
	Total  float64 `json:"total"`
	Slices []Slice `json:"slices"`
}

// Allocate splits fund value, stock value and cash into the four classes. Exited funds are out; an overdrawn account counts as zero cash.
func Allocate(fs []funds.Fund, stockValue, cash float64, t *Target) Allocation {
	by := map[string]float64{"equity": stockValue, "cash": math.Max(cash, 0)}
	for i := range fs {
		if !fs[i].Exited() {
			by[classOf(fs[i].Type)] += funds.Value(&fs[i])
		}
	}
	a := Allocation{Slices: make([]Slice, 0, len(classes))}
	for _, c := range classes {
		a.Total += by[c]
	}
	for _, c := range classes {
		s := Slice{Class: c, Value: by[c]}
		if a.Total > 0 {
			s.Pct = by[c] / a.Total * 100
		}
		if t != nil {
			tp := t.of(c)
			d, sh := s.Pct-tp, tp/100*a.Total-by[c]
			s.Target, s.Drift, s.Shift = &tp, &d, &sh
		}
		a.Slices = append(a.Slices, s)
	}
	return a
}

// Return is an annualised rate for one holding; Rate is nil when there is too little history to annualise.
type Return struct {
	Key   string   `json:"key"` // fund id or stock symbol
	Name  string   `json:"name"`
	Rate  *float64 `json:"rate,omitempty"`
	Exact bool     `json:"exact"` // false when dated from the join date because no instalments were logged
}

type Returns struct {
	Portfolio *float64 `json:"portfolio,omitempty"`
	Funds     []Return `json:"funds"`
	Stocks    []Return `json:"stocks"`
}

func day(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", s, ist.Loc)
	return t, err == nil
}

// fundFlows is a fund's cash flows: what it held before the logged instalments (dated at its join date), each instalment, and
// today's value (or the sale proceeds on the exit date). ok is false when a date is missing.
func fundFlows(f *funds.Fund, now time.Time) (fl []xirr.Flow, exact, ok bool) {
	exact = len(f.Instalments) > 0
	var inst float64
	for _, in := range f.Instalments {
		if d, ok := day(in.Date); ok {
			fl, inst = append(fl, xirr.Flow{At: d, Amount: -in.Amount}), inst+in.Amount
		}
	}
	if base := f.Invested - inst; base > 0 {
		j, ok := day(f.JoinedAt)
		if !ok {
			return nil, false, false
		}
		fl = append(fl, xirr.Flow{At: j, Amount: -base})
	}
	end := now
	if f.Exited() {
		var ok bool
		if end, ok = day(f.ExitedAt); !ok {
			return nil, false, false
		}
	}
	return append(fl, xirr.Flow{At: end, Amount: funds.Value(f)}), exact, len(fl) > 0
}

func rate(fl []xirr.Flow) *float64 {
	if r, ok := xirr.Rate(fl); ok {
		return &r
	}
	return nil
}

// Rates computes XIRR per fund, per stock and for everything together (exited funds included: their sale is a flow too; cash is not).
func Rates(fs []funds.Fund, p *stocks.Portfolio, now time.Time) Returns {
	out := Returns{Funds: []Return{}, Stocks: []Return{}}
	var all []xirr.Flow
	for i := range fs {
		f := &fs[i]
		fl, exact, ok := fundFlows(f, now)
		if !ok {
			continue
		}
		all = append(all, fl...)
		out.Funds = append(out.Funds, Return{Key: f.ID.Hex(), Name: f.Name, Rate: rate(fl), Exact: exact})
	}
	for _, pos := range p.Positions {
		var fl []xirr.Flow
		for _, l := range pos.Lots {
			if d, ok := day(l.BuyDate); ok {
				fl = append(fl, xirr.Flow{At: d, Amount: -l.Quantity * l.BuyPrice})
			}
		}
		fl = append(fl, xirr.Flow{At: now, Amount: pos.Value})
		all = append(all, fl...)
		out.Stocks = append(out.Stocks, Return{Key: pos.Symbol, Name: pos.Name, Rate: rate(fl), Exact: true})
	}
	if len(all) > 0 {
		out.Portfolio = rate(all)
	}
	sort.Slice(out.Funds, func(i, j int) bool { return out.Funds[i].Name < out.Funds[j].Name })
	return out
}
