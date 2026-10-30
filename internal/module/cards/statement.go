package cards

import (
	"math"
	"time"

	"ledger-backend/internal/ist"
)

const (
	DefaultApr  = 42.0 // percent a year, 3.5% a month: typical for Indian cards when the user has not set one
	minDueShare = 5    // minimum amount due, percent of the unpaid bill
	remindDays  = 5    // "pay in full" reminder starts this many days before the due date
)

// Statement is the last closed bill and what is still owed on it. Estimated from transactions: bill payments are counted only when
// their SMS carries the card number. Money is paise.
type Statement struct {
	From        string `json:"from"` // first day billed
	To          string `json:"to"`   // statement date
	BilledPaise int64  `json:"billed_paise"`
	PaidPaise   int64  `json:"paid_paise"` // bill payments since the statement date
	UnpaidPaise int64  `json:"unpaid_paise"`
	MinDuePaise int64  `json:"min_due_paise"`
	DueDate     string `json:"due_date"`
	DaysToDue   int    `json:"days_to_due"` // negative once past
	Overdue     bool   `json:"overdue"`
	// Status is where the bill stands: no_due (nothing billed), paid, partial, unpaid or overdue.
	Status string  `json:"status"`
	Remind bool    `json:"remind"` // unpaid and due within remindDays (or overdue): pay in full
	Apr    float64 `json:"apr"`
	// One month of interest on what stays unpaid: after paying only the minimum, and after paying nothing.
	InterestIfMinPaise     int64 `json:"interest_if_min_paise"`
	InterestIfSkippedPaise int64 `json:"interest_if_skipped_paise"`
}

// AddUnpaidStatement counts what is still owed on the last bill as used, so paying it down frees the limit. It has no effect when the user
// entered the lender's outstanding (that already includes the bill) or there is no statement.
func (v *View) AddUnpaidStatement() {
	if v.Statement == nil || v.OutstandingPaise != nil {
		return
	}
	v.UsedPaise += v.Statement.UnpaidPaise
	if v.LimitPaise > 0 {
		a, u := max(v.LimitPaise-v.UsedPaise, 0), float64(v.UsedPaise)/float64(v.LimitPaise)
		v.AvailablePaise, v.Utilisation = &a, &u
	}
}

// dueDate is the first date after the statement date whose day-of-month is the card's due day (clamped to short months).
func dueDate(dueDay int, stmt time.Time) time.Time {
	for m := 0; ; m++ {
		first := time.Date(stmt.Year(), stmt.Month()+time.Month(m), 1, 0, 0, 0, 0, ist.Loc)
		d := time.Date(first.Year(), first.Month(), min(dueDay, first.AddDate(0, 1, -1).Day()), 0, 0, 0, 0, ist.Loc)
		if d.After(stmt) {
			return d
		}
	}
}

// BuildStatement is pure: billed is the net spend of the cycle that just closed, paid the bill payments since its statement date.
// Nil when the card lacks a billing or due day.
func BuildStatement(c Card, billed, paid int64, now time.Time) *Statement {
	if c.BillingDay == 0 || c.DueDay == 0 {
		return nil
	}
	to := CycleStart(c, now)
	billed = max(billed, 0)
	unpaid := max(billed-max(paid, 0), 0)
	due := dueDate(c.DueDay, to)
	s := &Statement{
		From: CycleStart(c, to.AddDate(0, 0, -1)).Format("2006-01-02"), To: to.Format("2006-01-02"),
		BilledPaise: billed, PaidPaise: max(paid, 0), UnpaidPaise: unpaid, MinDuePaise: (unpaid*minDueShare + 99) / 100,
		DueDate: due.Format("2006-01-02"), DaysToDue: int(math.Round(due.Sub(ist.Midnight(now)).Hours() / 24)), Apr: c.Apr,
	}
	if s.Apr == 0 {
		s.Apr = DefaultApr
	}
	s.Overdue = unpaid > 0 && s.DaysToDue < 0
	switch {
	case billed == 0:
		s.Status = "no_due"
	case unpaid == 0:
		s.Status = "paid"
	case s.Overdue:
		s.Status = "overdue"
	case s.PaidPaise > 0:
		s.Status = "partial"
	default:
		s.Status = "unpaid"
	}
	s.Remind = unpaid > 0 && s.DaysToDue <= remindDays
	month := func(p int64) int64 { return int64(float64(p)*s.Apr/1200 + 0.5) }
	s.InterestIfMinPaise, s.InterestIfSkippedPaise = month(unpaid-s.MinDuePaise), month(unpaid)
	return s
}
