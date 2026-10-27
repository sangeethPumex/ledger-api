// Package emis tracks loans, fixed-instalment purchases and card balances, including payments and extra principal payments.
package emis

import (
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
)

const Collection = "emis"

// Amounts are rupees as typed by the user.

type Payment struct {
	ID        string  `json:"id" bson:"id"`
	Date      string  `json:"date" bson:"date"`
	Kind      string  `json:"kind" bson:"kind"` // emi | extra
	Amount    float64 `json:"amount" bson:"amount"`
	Principal float64 `json:"principal" bson:"principal"`
	Interest  float64 `json:"interest" bson:"interest"`
	Fees      float64 `json:"fees" bson:"fees"`
	Reduced   float64 `json:"reduced" bson:"reduced"` // how much the balance fell
}

// Emi is one obligation. Kind is loan, purchase or card. Balance (outstanding principal, amount left, or card balance) is optional:
// without it the amount left is Amount x instalments left.
type Emi struct {
	ID       bson.ObjectID `json:"id" bson:"_id"`
	Name     string        `json:"name" bson:"name"`
	Kind     string        `json:"kind" bson:"kind"`
	Amount   float64       `json:"amount" bson:"amount"`
	DueDay   int           `json:"dueDay" bson:"dueDay"`
	Total    int           `json:"total" bson:"total"`
	Paid     int           `json:"paid" bson:"paid"`
	Rate     *float64      `json:"rate,omitempty" bson:"rate,omitempty"`
	Balance  *float64      `json:"balance,omitempty" bson:"balance,omitempty"`
	Original *float64      `json:"original,omitempty" bson:"original,omitempty"`
	Payments []Payment     `json:"payments,omitempty" bson:"payments,omitempty"`
}

// Input is what a client may set; payments change only through the payment routes.
type Input struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Amount   float64  `json:"amount"`
	DueDay   int      `json:"dueDay"`
	Total    int      `json:"total"`
	Paid     int      `json:"paid"`
	Rate     *float64 `json:"rate"`
	Balance  *float64 `json:"balance"`
	Original *float64 `json:"original"`
}

func bad(v ...float64) bool {
	for _, x := range v {
		if x < 0 || x != x {
			return true
		}
	}
	return false
}

func badp(v ...*float64) bool {
	for _, x := range v {
		if x != nil && bad(*x) {
			return true
		}
	}
	return false
}

func (in *Input) Validate() string {
	card := in.Kind == "card"
	switch {
	case strings.TrimSpace(in.Name) == "" || len(in.Name) > 100:
		return "name required (max 100 characters)"
	case in.Kind != "loan" && in.Kind != "purchase" && !card:
		return "kind must be loan, purchase or card"
	case in.Amount <= 0 || in.Amount != in.Amount:
		return "amount must be positive"
	case in.DueDay < 1 || in.DueDay > 31:
		return "dueDay must be 1-31"
	case card && in.Balance == nil:
		return "a card needs its current balance"
	case !card && in.Total < 1:
		return "total instalments must be at least 1"
	case in.Total < 0 || in.Paid < 0 || in.Paid > in.Total:
		return "paid must be between 0 and total"
	case badp(in.Rate, in.Balance, in.Original):
		return "rate, balance and original must not be negative"
	}
	return ""
}

func Left(e *Emi) float64 {
	if e.Balance != nil {
		return *e.Balance
	}
	return e.Amount * float64(e.Total-e.Paid)
}

func Active(e *Emi) bool {
	if e.Balance != nil {
		return *e.Balance > 0
	}
	return e.Paid < e.Total
}

func r2(v float64) float64 { return math.Round(v*100) / 100 }

// Split is the interest/principal division of one ordinary instalment: interest first, the rest to principal. An estimate, not the lender's figure.
func Split(e *Emi) (principal, interest float64) {
	if e.Kind == "loan" && e.Rate != nil && *e.Rate > 0 && e.Balance != nil {
		interest = math.Min(*e.Balance**e.Rate/1200, e.Amount)
	}
	return r2(e.Amount - interest), r2(interest)
}

// Apply records a payment: lowers the balance and, for instalments, raises the paid count.
func Apply(e *Emi, p Payment) {
	if e.Balance != nil {
		p.Reduced = r2(math.Min(p.Principal, *e.Balance))
		b := r2(*e.Balance - p.Reduced)
		e.Balance = &b
	}
	if p.Kind == "emi" && e.Kind != "card" && e.Paid < e.Total {
		e.Paid++
	}
	e.Payments = append([]Payment{p}, e.Payments...)
}

// Undo reverses the most recent payment; false when there is none.
func Undo(e *Emi) bool {
	if len(e.Payments) == 0 {
		return false
	}
	last := e.Payments[0]
	e.Payments = e.Payments[1:]
	if e.Balance != nil {
		b := r2(*e.Balance + last.Reduced)
		e.Balance = &b
	}
	if last.Kind == "emi" && e.Kind != "card" && e.Paid > 0 {
		e.Paid--
	}
	return true
}

// Payoff is the months to clear balance paying monthly at annualRate percent, and the interest paid on the way.
// ok is false when the payment never beats the interest.
func Payoff(balance, annualRate, monthly float64) (months int, interest float64, ok bool) {
	if balance <= 0 {
		return 0, 0, true
	}
	r := annualRate / 1200
	if r == 0 {
		return int(math.Ceil(balance / monthly)), 0, true
	}
	if monthly <= balance*r {
		return 0, 0, false
	}
	n := -math.Log(1-balance*r/monthly) / math.Log(1+r)
	return int(math.Ceil(n)), n*monthly - balance, true
}

// Impact estimates paying `extra` against principal now. ok is false without a loan rate and balance.
func Impact(e *Emi, extra float64) (interestSaved float64, monthsSaved, monthsLeft int, ok bool) {
	if e.Kind != "loan" || e.Rate == nil || *e.Rate <= 0 || e.Balance == nil {
		return 0, 0, 0, false
	}
	m0, i0, ok0 := Payoff(*e.Balance, *e.Rate, e.Amount)
	m1, i1, ok1 := Payoff(math.Max(*e.Balance-extra, 0), *e.Rate, e.Amount)
	if !ok0 || !ok1 {
		return 0, 0, 0, false
	}
	return r2(i0 - i1), m0 - m1, m1, true
}

// View is an Emi with the figures the UI shows.
type View struct {
	Emi
	Active          bool     `json:"active"`
	Left            float64  `json:"left"`
	InstalmentsLeft int      `json:"instalmentsLeft"`
	DaysToDue       int      `json:"daysToDue"`
	Repaid          *float64 `json:"repaid,omitempty"` // share of the original amount, when known
	PaidPrincipal   float64  `json:"paidPrincipal"`
	PaidInterest    float64  `json:"paidInterest"` // interest and fees
}

func ViewOf(e Emi, now time.Time) View {
	v := View{Emi: e, Active: Active(&e), Left: r2(Left(&e)), InstalmentsLeft: max(e.Total-e.Paid, 0), DaysToDue: ist.DaysUntil(e.DueDay, now)}
	if e.Original != nil && *e.Original > 0 && e.Balance != nil {
		r := 1 - *e.Balance / *e.Original
		v.Repaid = &r
	}
	for _, p := range e.Payments {
		v.PaidPrincipal += p.Principal
		v.PaidInterest += p.Interest + p.Fees
	}
	return v
}
