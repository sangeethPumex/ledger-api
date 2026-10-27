// Package funds tracks mutual fund holdings, their latest NAV (mfapi.in) and SIPs.
package funds

import (
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
)

const Collection = "funds"

type Sip struct {
	Amount float64 `json:"amount" bson:"amount"`
	Day    int     `json:"day" bson:"day"`
	Status string  `json:"status,omitempty" bson:"status,omitempty"` // active (or empty) | paused | cancelled
}

// Live is true for a SIP that still debits: counted in monthly totals, reminders and instalments.
func (s *Sip) Live() bool { return s != nil && (s.Status == "" || s.Status == "active") }

// Fund is a holding. Rupees; Code is the mfapi.in scheme code. Units, AvgNav and Invested are typed from the broker app.
type Fund struct {
	ID         bson.ObjectID `json:"id" bson:"_id"`
	Code       int64         `json:"code" bson:"code"`
	Name       string        `json:"name" bson:"name"`
	FundHouse  string        `json:"fundHouse,omitempty" bson:"fundHouse,omitempty"`
	Platform   string        `json:"platform" bson:"platform"`
	AccountID  string        `json:"accountId,omitempty" bson:"accountId,omitempty"` // bank account the SIP debits
	Type       string        `json:"type" bson:"type"`                               // Equity | Debt | Metal | Foreign
	Membership string        `json:"membership" bson:"membership"`                   // Genius | Standalone
	Units      float64       `json:"units" bson:"units"`
	AvgNav     float64       `json:"avgNav" bson:"avgNav"`
	Invested   float64       `json:"invested" bson:"invested"`
	Nav        *float64      `json:"nav,omitempty" bson:"nav,omitempty"`
	NavDate    string        `json:"navDate,omitempty" bson:"navDate,omitempty"` // YYYY-MM-DD
	Sip        *Sip          `json:"sip,omitempty" bson:"sip,omitempty"`
	// Status is active (or empty) or exited. A Genius fund leaves the basket at a monthly rebalance: it stops counting in totals and
	// SIPs, and keeps what it was sold for so the realised gain stays visible. JoinedAt and ExitedAt are YYYY-MM-DD.
	Status   string   `json:"status,omitempty" bson:"status,omitempty"`
	JoinedAt string   `json:"joinedAt,omitempty" bson:"joinedAt,omitempty"`
	ExitedAt string   `json:"exitedAt,omitempty" bson:"exitedAt,omitempty"`
	Proceeds *float64 `json:"proceeds,omitempty" bson:"proceeds,omitempty"` // rupees received on exit
}

func (f *Fund) Exited() bool { return f.Status == "exited" }

// OwnSip is the fund's own SIP. A Genius fund has none: the whole basket is bought with one SIP, however many funds it holds.
func (f *Fund) OwnSip() *Sip {
	if f.Membership == "Genius" {
		return nil
	}
	return f.Sip
}

// Input is what a client may set; name, fund house and NAV come from the mfapi.in lookup.
type Input struct {
	Code       int64  `json:"code"`
	Platform   string `json:"platform"`
	AccountID  string `json:"accountId"`
	Type       string `json:"type"`
	Membership string `json:"membership"`
	// Units, AvgNav and Invested are optional; Invested defaults to Units x AvgNav.
	Units    float64  `json:"units"`
	AvgNav   float64  `json:"avgNav"`
	Invested *float64 `json:"invested"`
	Sip      *Sip     `json:"sip"`
	JoinedAt string   `json:"joinedAt"` // optional YYYY-MM-DD; a new fund defaults to today
}

// ExitReq moves a fund out of the portfolio. Proceeds default to what the units are worth now; Date to today.
type ExitReq struct {
	Date     string   `json:"date"`
	Proceeds *float64 `json:"proceeds"`
}

func validDate(s string) bool { _, err := time.Parse("2006-01-02", s); return err == nil }

func (r *ExitReq) Validate() string {
	switch {
	case r.Date != "" && !validDate(r.Date):
		return "date must be YYYY-MM-DD"
	case r.Proceeds != nil && neg(*r.Proceeds):
		return "proceeds must not be negative"
	}
	return ""
}

func neg(v ...float64) bool {
	for _, x := range v {
		if x < 0 || x != x {
			return true
		}
	}
	return false
}

func (in *Input) Validate() string {
	switch {
	case in.Code <= 0:
		return "scheme code required"
	case strings.TrimSpace(in.Platform) == "":
		return "platform required"
	case in.Type != "Equity" && in.Type != "Debt" && in.Type != "Metal" && in.Type != "Foreign":
		return "type must be Equity, Debt, Metal or Foreign"
	case in.Membership != "Genius" && in.Membership != "Standalone":
		return "membership must be Genius or Standalone"
	case in.Units <= 0 || in.AvgNav <= 0 || neg(in.Units, in.AvgNav):
		return "units and avgNav must be positive"
	case in.Invested != nil && neg(*in.Invested):
		return "invested must not be negative"
	case in.AccountID != "" && !validID(in.AccountID):
		return "accountId is not a valid account id"
	case in.Sip != nil && in.Membership == "Genius":
		return "Genius funds share one basket SIP; set it on the Genius panel"
	case in.Sip != nil && in.Sip.Status != "" && in.Sip.Status != "active" && in.Sip.Status != "paused" && in.Sip.Status != "cancelled":
		return "sip status must be active, paused or cancelled"
	case in.Sip != nil && (in.Sip.Amount <= 0 || in.Sip.Day < 1 || in.Sip.Day > 31):
		return "sip needs a positive amount and a day 1-31"
	case in.JoinedAt != "" && !validDate(in.JoinedAt):
		return "joinedAt must be YYYY-MM-DD"
	}
	return ""
}

func validID(s string) bool { _, err := bson.ObjectIDFromHex(s); return err == nil }

func (in *Input) invested() float64 {
	if in.Invested != nil {
		return *in.Invested
	}
	return in.Units * in.AvgNav
}

// Value is units x latest NAV, or the invested amount until a NAV has been fetched.
// An exited fund is worth what it was sold for.
func Value(f *Fund) float64 {
	if f.Exited() {
		if f.Proceeds != nil {
			return *f.Proceeds
		}
		return 0
	}
	if f.Nav != nil && *f.Nav > 0 {
		return f.Units * *f.Nav
	}
	return f.Invested
}

// View is a Fund with the figures the UI shows.
type View struct {
	Fund
	Active bool    `json:"active"`
	Value  float64 `json:"value"`
	Pnl    float64 `json:"pnl"` // realised once exited

	ReturnPct *float64 `json:"returnPct,omitempty"`
	SipInDays *int     `json:"sipInDays,omitempty"`
}

func ViewOf(f Fund, now time.Time) View {
	v := View{Fund: f, Active: !f.Exited(), Value: Value(&f)}
	v.Pnl = v.Value - f.Invested
	if f.Invested > 0 {
		r := v.Pnl / f.Invested
		v.ReturnPct = &r
	}
	v.Fund.Sip = f.OwnSip()
	if v.Sip.Live() {
		d := ist.DaysUntil(v.Sip.Day, now)
		v.SipInDays = &d
	}
	return v
}

// Exit marks the fund as sold out of the portfolio on date for proceeds, and ends its SIP.
func Exit(f *Fund, date string, proceeds float64) {
	f.Status, f.ExitedAt, f.Proceeds = "exited", date, &proceeds
	if f.Sip != nil {
		f.Sip.Status = "cancelled"
	}
}

// Reenter puts an exited fund back in the portfolio. Units and invested are whatever they were; edit them to the new holding.
func Reenter(f *Fund) {
	f.Status, f.ExitedAt, f.Proceeds = "", "", nil
}

// Move is one fund entering or leaving the Genius basket.
type Move struct {
	Name   string  `json:"name"`
	Kind   string  `json:"kind"` // in | out
	Date   string  `json:"date"`
	Amount float64 `json:"amount"` // invested on the way in, proceeds on the way out
}

// Genius summarises the ET Money Genius basket: what is in it now, what left, and the recent rebalance moves.
type Genius struct {
	Active        int     `json:"active"`
	Value         float64 `json:"value"`
	Invested      float64 `json:"invested"`
	Pnl           float64 `json:"pnl"`
	Exited        int     `json:"exited"`
	Realised      float64 `json:"realised"`                // gain or loss booked on funds that left
	LastRebalance string  `json:"lastRebalance,omitempty"` // date of the latest move in or out
	Moves         []Move  `json:"moves"`                   // last 60 days, newest first
	// Sip is the single monthly SIP for the basket, the same whether it holds two funds or six.
	Sip       *GeniusSip `json:"sip,omitempty"`
	SipInDays *int       `json:"sipInDays,omitempty"`
}

const movesWindow = 60 * 24 * time.Hour

// GeniusOf is pure: every fund in, the Genius figures out.
func GeniusOf(fs []Fund, now time.Time) Genius {
	g := Genius{Moves: []Move{}}
	since := now.In(ist.Loc).Add(-movesWindow).Format("2006-01-02")
	note := func(m Move) {
		if m.Date > g.LastRebalance {
			g.LastRebalance = m.Date
		}
		if m.Date >= since {
			g.Moves = append(g.Moves, m)
		}
	}
	for i := range fs {
		f := &fs[i]
		if f.Membership != "Genius" {
			continue
		}
		if f.Exited() {
			g.Exited++
			g.Realised += Value(f) - f.Invested
			if f.ExitedAt != "" {
				note(Move{Name: f.Name, Kind: "out", Date: f.ExitedAt, Amount: Value(f)})
			}
		} else {
			g.Active++
			g.Value += Value(f)
			g.Invested += f.Invested
		}
		if f.JoinedAt != "" {
			note(Move{Name: f.Name, Kind: "in", Date: f.JoinedAt, Amount: f.Invested})
		}
	}
	g.Pnl = g.Value - g.Invested
	sort.SliceStable(g.Moves, func(i, j int) bool { return g.Moves[i].Date > g.Moves[j].Date })
	return g
}

// AddSip books one SIP instalment at the latest NAV: more units, more invested, average NAV recomputed.
// ok is false without a SIP or a NAV.
func AddSip(f *Fund) bool {
	if f.Exited() || !f.OwnSip().Live() || f.Nav == nil || *f.Nav <= 0 {
		return false
	}
	buy(f, f.Sip.Amount)
	return true
}

// buy adds `amount` rupees at the latest NAV and recomputes the average.
func buy(f *Fund, amount float64) {
	f.Units += amount / *f.Nav
	f.Invested += amount
	f.AvgNav = f.Invested / f.Units
}

// AddGeniusSip spreads one basket instalment over the active Genius funds in proportion to what each is worth now, and books it.
// ET Money's real split follows its own model weights, so this is an estimate. False, changing nothing, when there is no fund,
// no live SIP or a fund without a NAV.
func AddGeniusSip(fs []*Fund, sip *Sip) bool {
	var basket []*Fund
	var total float64
	for _, f := range fs {
		if f.Membership != "Genius" || f.Exited() {
			continue
		}
		if f.Nav == nil || *f.Nav <= 0 {
			return false
		}
		basket, total = append(basket, f), total+Value(f)
	}
	if len(basket) == 0 || !sip.Live() {
		return false
	}
	for _, f := range basket {
		w := 1 / float64(len(basket))
		if total > 0 {
			w = Value(f) / total
		}
		buy(f, sip.Amount*w)
	}
	return true
}

// GeniusSip is the one SIP for the whole Genius basket, with the bank account it debits.
type GeniusSip struct {
	Sip
	AccountID string `json:"accountId,omitempty" bson:"accountId,omitempty"`
}

// Validate returns a message when the basket SIP is unusable.
func (g *GeniusSip) Validate() string {
	switch {
	case g.Amount <= 0 || neg(g.Amount) || g.Day < 1 || g.Day > 31:
		return "sip needs a positive amount and a day 1-31"
	case g.Status != "" && g.Status != "active" && g.Status != "paused" && g.Status != "cancelled":
		return "sip status must be active, paused or cancelled"
	case g.AccountID != "" && !validID(g.AccountID):
		return "accountId is not a valid account id"
	}
	return ""
}
