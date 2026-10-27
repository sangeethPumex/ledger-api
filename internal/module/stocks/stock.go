// Package stocks tracks what you own (symbol, quantity, buy price, buy date) and values it with quotes from a PriceProvider.
// Prices are never part of a holding; they live in a small cache collection that is only a fallback and a rate-limit shield.
package stocks

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
)

const (
	Collection      = "stocks"
	QuoteCollection = "stock_quotes"
)

// Quote is a symbol's last known price and previous close, in the symbol's own currency (rupees for .NS/.BO).
type Quote struct {
	Symbol    string    `bson:"_id"`
	Name      string    `bson:"name"` // company name; cached with the quote, not part of the holding
	// Domain is the company website's host (ril.com), used to look up a logo. DomainTried is set once Yahoo was asked,
	// so a symbol without a website is not re-asked on every refresh.
	Domain      string `bson:"domain,omitempty"`
	DomainTried bool   `bson:"domainTried,omitempty"`
	Price     float64   `bson:"price"`
	PrevClose float64   `bson:"prevClose"`
	FetchedAt time.Time `bson:"fetchedAt"`
}

// PriceProvider returns quotes for the symbols it could price; a missing symbol is simply absent from the map.
// Swap the implementation (Yahoo today) without touching the rest.
type PriceProvider interface {
	Quotes(ctx context.Context, symbols []string) (map[string]Quote, error)
}

// Lot is one purchase. Rupees.
type Lot struct {
	ID       bson.ObjectID `json:"id" bson:"_id"`
	Symbol   string        `json:"symbol" bson:"symbol"` // Yahoo form: RELIANCE.NS, TCS.BO
	Quantity float64       `json:"quantity" bson:"quantity"`
	BuyPrice float64       `json:"buyPrice" bson:"buyPrice"`
	BuyDate  string        `json:"buyDate" bson:"buyDate"` // YYYY-MM-DD
	Platform string        `json:"platform,omitempty" bson:"platform,omitempty"` // broker it was bought on: Groww, Motilal Oswal, Geojit...
}

type Input struct {
	Symbol   string  `json:"symbol"`
	Quantity float64 `json:"quantity"`
	BuyPrice float64 `json:"buyPrice"`
	BuyDate  string  `json:"buyDate"`
	Platform string  `json:"platform"`
}

// The symbol goes into a URL path, so keep it to what Yahoo tickers actually use.
var symbolRe = regexp.MustCompile(`^[A-Z0-9&^-]{1,20}\.(NS|BO)$`)

// Validate normalises the symbol and returns a message if the input is unusable.
func (in *Input) Validate(now time.Time) string {
	in.Symbol = strings.ToUpper(strings.TrimSpace(in.Symbol))
	in.Platform = strings.TrimSpace(in.Platform)
	d, err := time.Parse("2006-01-02", in.BuyDate)
	switch {
	case !symbolRe.MatchString(in.Symbol):
		return "symbol needs an exchange suffix, e.g. RELIANCE.NS or TCS.BO"
	case !(in.Quantity > 0) || !(in.BuyPrice > 0): // also rejects NaN
		return "quantity and buy price must be positive"
	case err != nil:
		return "buy date must be YYYY-MM-DD"
	case d.After(now.In(ist.Loc)):
		return "buy date is in the future"
	case utf8.RuneCountInString(in.Platform) > 40:
		return "platform is too long"
	}
	return ""
}

// LotView is a lot with its own figures; Pnl and Value use the quote when there is one.
type LotView struct {
	Lot
	Value     float64 `json:"value"`
	Pnl       float64 `json:"pnl"`
	ReturnPct float64 `json:"returnPct"`
}

// Position is every lot of one symbol, rolled up.
type Position struct {
	Symbol     string    `json:"symbol"`
	Name       string    `json:"name,omitempty"`
	Domain     string    `json:"domain,omitempty"`
	Quantity   float64   `json:"quantity"`
	AvgPrice   float64   `json:"avgPrice"`
	Invested   float64   `json:"invested"`
	Price      *float64  `json:"price,omitempty"` // nil when no quote has ever been fetched
	PrevClose  *float64  `json:"prevClose,omitempty"`
	AsOf       time.Time `json:"asOf,omitempty"`
	Value      float64   `json:"value"` // invested while unpriced
	Pnl        float64   `json:"pnl"`
	PnlPct     float64   `json:"pnlPct"`
	DayChange  float64   `json:"dayChange"`
	DayPct     float64   `json:"dayPct"`
	Allocation float64   `json:"allocation"` // share of total value, 0..1
	Lots       []LotView `json:"lots"`
	Stale      bool      `json:"stale"` // priced from cache because the live fetch failed
}

type Summary struct {
	Invested  float64    `json:"invested"`
	Value     float64    `json:"value"`
	Pnl       float64    `json:"pnl"`
	PnlPct    float64    `json:"pnlPct"`
	DayChange float64    `json:"dayChange"`
	DayPct    float64    `json:"dayPct"`
	AsOf      *time.Time `json:"asOf,omitempty"` // oldest quote used
	Stale     bool       `json:"stale"`          // some quotes are older than the freshness rule allows
	Unpriced  int        `json:"unpriced"`       // positions valued at cost for lack of any quote
}

type Portfolio struct {
	Summary   Summary    `json:"summary"`
	Positions []Position `json:"positions"`
}

func pct(part, whole float64) float64 {
	if whole == 0 {
		return 0
	}
	return part / whole
}

// Compute rolls lots up per symbol and values them. Pure: quotes in, numbers out.
// freshAt is the oldest FetchedAt still considered current; older quotes are used but flagged stale.
func Compute(lots []Lot, quotes map[string]Quote, freshAt time.Time) Portfolio {
	by := map[string]*Position{}
	for _, l := range lots {
		p := by[l.Symbol]
		if p == nil {
			p = &Position{Symbol: l.Symbol, Lots: []LotView{}}
			by[l.Symbol] = p
		}
		p.Quantity += l.Quantity
		p.Invested += l.Quantity * l.BuyPrice
		p.Lots = append(p.Lots, LotView{Lot: l})
	}

	out := Portfolio{Positions: make([]Position, 0, len(by))}
	s := &out.Summary
	var prevValue float64 // yesterday's value of priced positions, the base for the day %
	for _, p := range by {
		p.AvgPrice = p.Invested / p.Quantity
		q, priced := quotes[p.Symbol]
		priced = priced && q.Price > 0
		if priced {
			p.Name, p.Domain, p.Price, p.PrevClose, p.AsOf = q.Name, q.Domain, &q.Price, &q.PrevClose, q.FetchedAt
			p.Value = p.Quantity * q.Price
			if q.PrevClose > 0 {
				p.DayChange = p.Quantity * (q.Price - q.PrevClose)
				p.DayPct = pct(q.Price-q.PrevClose, q.PrevClose)
				prevValue += p.Quantity * q.PrevClose
			}
			p.Stale = q.FetchedAt.Before(freshAt)
			if s.AsOf == nil || q.FetchedAt.Before(*s.AsOf) {
				t := q.FetchedAt
				s.AsOf = &t
			}
			s.Stale = s.Stale || p.Stale
		} else {
			p.Value = p.Invested
			s.Unpriced++
		}
		p.Pnl = p.Value - p.Invested
		p.PnlPct = pct(p.Pnl, p.Invested)
		for i := range p.Lots {
			l := &p.Lots[i]
			cost := l.Quantity * l.BuyPrice
			l.Value = cost
			if priced {
				l.Value = l.Quantity * q.Price
			}
			l.Pnl, l.ReturnPct = l.Value-cost, pct(l.Value-cost, cost)
		}
		sort.Slice(p.Lots, func(i, j int) bool { return p.Lots[i].BuyDate < p.Lots[j].BuyDate })
		s.Invested += p.Invested
		s.Value += p.Value
		s.DayChange += p.DayChange
		out.Positions = append(out.Positions, *p)
	}
	s.Pnl, s.PnlPct, s.DayPct = s.Value-s.Invested, pct(s.Value-s.Invested, s.Invested), pct(s.DayChange, prevValue)
	for i := range out.Positions {
		out.Positions[i].Allocation = pct(out.Positions[i].Value, s.Value)
	}
	sort.Slice(out.Positions, func(i, j int) bool { return out.Positions[i].Value > out.Positions[j].Value })
	return out
}

// Indian cash market hours, Mon-Fri 09:15-15:30 IST. Exchange holidays are not modelled: on one, quotes just stay unchanged.
func marketOpen(now time.Time) bool {
	n := now.In(ist.Loc)
	if n.Weekday() == time.Saturday || n.Weekday() == time.Sunday {
		return false
	}
	m := n.Hour()*60 + n.Minute()
	return m >= 9*60+15 && m < 15*60+30
}

const liveTTL = 10 * time.Minute

// freshAt is the oldest FetchedAt that still counts as current: a few minutes back while the market trades,
// otherwise the last close (+15 min grace for Yahoo's lag), so after the bell each symbol is fetched once.
func freshAt(now time.Time) time.Time {
	if marketOpen(now) {
		return now.Add(-liveTTL)
	}
	d := ist.Midnight(now)
	if now.Before(d.Add(15*time.Hour + 45*time.Minute)) {
		d = d.AddDate(0, 0, -1)
	}
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d.Add(15*time.Hour + 45*time.Minute)
}
