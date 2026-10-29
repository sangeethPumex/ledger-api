package cards

import (
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/emis"
)

const Collection = "credit_cards"

// Kinds of card. An empty kind (cards made before kinds existed) means credit.
const (
	KindCredit   = "credit"
	KindPayLater = "paylater"
)

// Card is a credit card or a pay-later line (Amazon Pay Later, Simpl...). Bank and Last4 are optional; without Last4 no SMS is linked to it.
// Money is paise, like transactions. BillingDay is the statement date and DueDay the last day to pay, both 1-31 or 0 when unknown.
type Card struct {
	ID         bson.ObjectID `bson:"_id" json:"id"`
	Name       string        `bson:"name" json:"name"`
	Kind       string        `bson:"kind,omitempty" json:"kind"`
	Bank       string        `bson:"bank,omitempty" json:"bank,omitempty"`
	Last4      string        `bson:"last4,omitempty" json:"last4,omitempty"`
	LimitPaise int64         `bson:"limit_paise,omitempty" json:"limit_paise,omitempty"`
	BillingDay int           `bson:"billing_day,omitempty" json:"billing_day,omitempty"`
	DueDay     int           `bson:"due_day,omitempty" json:"due_day,omitempty"`
	// Apr is the yearly interest rate in percent charged on an unpaid bill. Zero means DefaultApr.
	Apr float64 `bson:"apr,omitempty" json:"apr,omitempty"`
	// OutstandingPaise is what the lender's app says is owed. When set it replaces the figure worked out from transactions,
	// which is the only way to track a line whose SMS carry no card number.
	OutstandingPaise *int64 `bson:"outstanding_paise,omitempty" json:"outstanding_paise,omitempty"`
	// SharedWith names the card that owns the limit this one draws from (two cards from one bank often share a limit).
	// A card that shares has no limit of its own, and the owner cannot itself share: no chains.
	SharedWith *bson.ObjectID `bson:"shared_with,omitempty" json:"shared_with,omitempty"`
	CreatedAt  time.Time      `bson:"created_at" json:"created_at"`
}

const maxPaise = 10_000_000_000 // 10 crore rupees, same sanity cap as transactions

func validLast4(l string) bool {
	return l == "" || (len(l) == 4 && strings.Trim(l, "0123456789") == "")
}
func validDay(d int) bool     { return d >= 0 && d <= 31 }
func validApr(a float64) bool { return a >= 0 && a <= 100 } // also false for NaN
func validKind(k string) bool { return k == KindCredit || k == KindPayLater }

// Validate returns a message when the card is unusable.
func (c *Card) Validate() string {
	switch {
	case strings.TrimSpace(c.Name) == "" || len(c.Name) > 100:
		return "name required (max 100 characters)"
	case c.Kind != "" && !validKind(c.Kind):
		return "kind must be credit or paylater"
	case !validLast4(c.Last4):
		return "last4 must be 4 digits"
	case c.LimitPaise < 0 || c.LimitPaise > maxPaise:
		return "limit is out of range"
	case !validDay(c.BillingDay) || !validDay(c.DueDay):
		return "billing and due days must be 1-31"
	case !validApr(c.Apr):
		return "apr must be 0-100"
	case c.OutstandingPaise != nil && (*c.OutstandingPaise < 0 || *c.OutstandingPaise > maxPaise):
		return "outstanding is out of range"
	}
	return ""
}

// Patch changes the given fields; nil leaves one alone. An empty string or zero clears an optional field
// (a negative OutstandingPaise clears the manual outstanding).
type Patch struct {
	Name             *string  `json:"name"`
	Kind             *string  `json:"kind"`
	Bank             *string  `json:"bank"`
	Last4            *string  `json:"last4"`
	LimitPaise       *int64   `json:"limit_paise"`
	BillingDay       *int     `json:"billing_day"`
	DueDay           *int     `json:"due_day"`
	Apr              *float64 `json:"apr"`
	OutstandingPaise *int64   `json:"outstanding_paise"`
	SharedWith       *string  `json:"shared_with"` // a card id; empty clears
}

func (p *Patch) Validate() string {
	switch {
	case p.Name != nil && (strings.TrimSpace(*p.Name) == "" || len(*p.Name) > 100):
		return "name cannot be empty (max 100 characters)"
	case p.Kind != nil && !validKind(*p.Kind):
		return "kind must be credit or paylater"
	case p.Last4 != nil && !validLast4(*p.Last4):
		return "last4 must be 4 digits"
	case p.LimitPaise != nil && (*p.LimitPaise < 0 || *p.LimitPaise > maxPaise):
		return "limit is out of range"
	case p.BillingDay != nil && !validDay(*p.BillingDay), p.DueDay != nil && !validDay(*p.DueDay):
		return "billing and due days must be 1-31"
	case p.Apr != nil && !validApr(*p.Apr):
		return "apr must be 0-100"
	case p.OutstandingPaise != nil && *p.OutstandingPaise > maxPaise:
		return "outstanding is out of range"
	case p.SharedWith != nil && *p.SharedWith != "" && !validID(*p.SharedWith):
		return "shared_with is not a valid card id"
	}
	return ""
}

// LinkedEmi is a loan or instalment plan charged to the card. Rupees, as in the emis module.
type LinkedEmi struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Monthly float64 `json:"monthly"`
	Left    float64 `json:"left"`
	Active  bool    `json:"active"`
}

// View is a card with its figures, all worked out here. Used is an estimate: this cycle's spends plus what is left on linked loans,
// unless the user entered the lender's outstanding. Unpaid bills from earlier cycles are not known.
type View struct {
	Card
	CycleStart         string      `json:"cycle_start"` // first day of the running billing cycle, YYYY-MM-DD
	CycleSpentPaise    int64       `json:"cycle_spent_paise"`
	CycleTxns          int         `json:"cycle_txns"`
	LinkedEmis         []LinkedEmi `json:"linked_emis"`
	LinkedMonthlyPaise int64       `json:"linked_monthly_paise"` // instalments due each month on active linked loans
	LinkedLeftPaise    int64       `json:"linked_left_paise"`
	UsedPaise          int64       `json:"used_paise"`
	AvailablePaise     *int64      `json:"available_paise,omitempty"` // nil without a limit
	Utilisation        *float64    `json:"utilisation,omitempty"`     // used / limit, may pass 1
	DaysToBilling      *int        `json:"days_to_billing,omitempty"`
	DaysToDue          *int        `json:"days_to_due,omitempty"`
	Statement          *Statement  `json:"statement,omitempty"` // nil without billing and due days
	// Set by ApplyShared. For a card in a shared group LimitPaise is the group's limit, and Available and Utilisation are the group's.
	SharedWithName string   `json:"shared_with_name,omitempty"` // the card that owns the limit, on a card that shares
	SharedCards    []string `json:"shared_cards"`               // names of the other cards in the group
	GroupUsedPaise int64    `json:"group_used_paise,omitempty"`
	// The bill payments linked to this card from transactions: how many, and the latest.
	Payments         int        `json:"payments"`
	LastPaymentAt    *time.Time `json:"last_payment_at,omitempty"`
	LastPaymentPaise int64      `json:"last_payment_paise,omitempty"`
}

func paise(rupees float64) int64 { return int64(rupees*100 + 0.5) }

// CycleStart is when the running billing cycle began: the last statement date, or the 1st of the month without one.
func CycleStart(c Card, now time.Time) time.Time {
	if c.BillingDay > 0 {
		return ist.LastDay(c.BillingDay, now)
	}
	n := now.In(ist.Loc)
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, ist.Loc)
}

// ApplyShared gives every card in a shared group the group's limit, used amount, availability and utilisation.
// A card pointing at a missing or itself-sharing owner is treated as standalone. Pure: views in, the same views updated.
func ApplyShared(vs []View) {
	byID := map[bson.ObjectID]int{}
	for i := range vs {
		byID[vs[i].ID] = i
	}
	groups := map[int][]int{} // owner index -> member indexes
	for i := range vs {
		if vs[i].SharedWith == nil {
			continue
		}
		if o, ok := byID[*vs[i].SharedWith]; ok && o != i && vs[o].SharedWith == nil {
			groups[o] = append(groups[o], i)
		} else {
			vs[i].SharedWith = nil
		}
	}
	for o, members := range groups {
		all := append([]int{o}, members...)
		limit := vs[o].LimitPaise
		var used int64
		for _, i := range all {
			used += vs[i].UsedPaise
		}
		for _, i := range all {
			v := &vs[i]
			v.LimitPaise, v.GroupUsedPaise = limit, used
			if i != o {
				v.SharedWithName = vs[o].Name
			}
			for _, j := range all {
				if j != i {
					v.SharedCards = append(v.SharedCards, vs[j].Name)
				}
			}
			v.AvailablePaise, v.Utilisation = nil, nil
			if limit > 0 {
				a, u := max(limit-used, 0), float64(used)/float64(limit)
				v.AvailablePaise, v.Utilisation = &a, &u
			}
		}
	}
}

// Compute is pure: the card, this cycle's net spend and the loans linked to it in, the figures out.
func Compute(c Card, spent int64, txns int, linked []emis.Emi, now time.Time) View {
	v := View{Card: c, SharedCards: []string{}, CycleStart: CycleStart(c, now).Format("2006-01-02"), CycleSpentPaise: max(spent, 0), CycleTxns: txns, LinkedEmis: []LinkedEmi{}}
	if v.Kind == "" {
		v.Kind = KindCredit
	}
	for i := range linked {
		e := &linked[i]
		l := LinkedEmi{ID: e.ID.Hex(), Name: e.Name, Monthly: e.Amount, Left: emis.Left(e), Active: emis.Active(e)}
		if l.Active {
			v.LinkedMonthlyPaise += paise(l.Monthly)
			v.LinkedLeftPaise += paise(l.Left)
		}
		v.LinkedEmis = append(v.LinkedEmis, l)
	}
	v.UsedPaise = v.CycleSpentPaise + v.LinkedLeftPaise
	if c.OutstandingPaise != nil {
		v.UsedPaise = *c.OutstandingPaise
	}
	if c.LimitPaise > 0 {
		a, u := max(c.LimitPaise-v.UsedPaise, 0), float64(v.UsedPaise)/float64(c.LimitPaise)
		v.AvailablePaise, v.Utilisation = &a, &u
	}
	if c.BillingDay > 0 {
		d := ist.DaysUntil(c.BillingDay, now)
		v.DaysToBilling = &d
	}
	if c.DueDay > 0 {
		d := ist.DaysUntil(c.DueDay, now)
		v.DaysToDue = &d
	}
	return v
}

func validID(s string) bool { _, err := bson.ObjectIDFromHex(s); return err == nil }
