// Package funds tracks mutual fund holdings, their latest NAV (mfapi.in) and SIPs.
package funds

import (
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
	case in.Sip != nil && in.Sip.Status != "" && in.Sip.Status != "active" && in.Sip.Status != "paused" && in.Sip.Status != "cancelled":
		return "sip status must be active, paused or cancelled"
	case in.Sip != nil && (in.Sip.Amount <= 0 || in.Sip.Day < 1 || in.Sip.Day > 31):
		return "sip needs a positive amount and a day 1-31"
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
func Value(f *Fund) float64 {
	if f.Nav != nil && *f.Nav > 0 {
		return f.Units * *f.Nav
	}
	return f.Invested
}

// View is a Fund with the figures the UI shows.
type View struct {
	Fund
	Value     float64  `json:"value"`
	Pnl       float64  `json:"pnl"`
	ReturnPct *float64 `json:"returnPct,omitempty"`
	SipInDays *int     `json:"sipInDays,omitempty"`
}

func ViewOf(f Fund, now time.Time) View {
	v := View{Fund: f, Value: Value(&f)}
	v.Pnl = v.Value - f.Invested
	if f.Invested > 0 {
		r := v.Pnl / f.Invested
		v.ReturnPct = &r
	}
	if f.Sip.Live() {
		d := ist.DaysUntil(f.Sip.Day, now)
		v.SipInDays = &d
	}
	return v
}

// AddSip books one SIP instalment at the latest NAV: more units, more invested, average NAV recomputed.
// ok is false without a SIP or a NAV.
func AddSip(f *Fund) bool {
	if !f.Sip.Live() || f.Nav == nil || *f.Nav <= 0 {
		return false
	}
	f.Units += f.Sip.Amount / *f.Nav
	f.Invested += f.Sip.Amount
	f.AvgNav = f.Invested / f.Units
	return true
}
