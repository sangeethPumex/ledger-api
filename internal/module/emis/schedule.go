package emis

import (
	"context"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
)

const day = "2006-01-02"

// Row is one billing period of the schedule: what the statement says for past periods, a projection for the rest.
// The percentages are shares of everything paid in the period (EMI + extra).
type Row struct {
	Period
	PaidOn       string  `json:"paidOn"` // the day the EMI for this period is debited: the day after the period ends
	Future       bool    `json:"future"`
	Rate         float64 `json:"rate"`
	PrincipalPct float64 `json:"principalPct"`
	InterestPct  float64 `json:"interestPct"`
}

type Totals struct {
	Interest  float64 `json:"interest"`
	EMI       float64 `json:"emi"`
	Extra     float64 `json:"extra"`
	Principal float64 `json:"principal"`
}

type Schedule struct {
	Available bool         `json:"available"` // needs a loan with a rate and a balance
	Rows      []Row        `json:"rows"`
	Past      Totals       `json:"past"`
	Ahead     Totals       `json:"ahead"`
	Rate      float64      `json:"rate"`
	RateType  string       `json:"rateType"`
	Rates     []RateChange `json:"rates"`
	Months    int          `json:"months"`    // periods still to pay
	ClearsOn  string       `json:"clearsOn"`  // end of the last period
	NeverEnds bool         `json:"neverEnds"` // the EMI does not cover the interest
	Note      string       `json:"note"`
}

// paidOn is the day after a period ends, which is when its EMI goes out.
func paidOn(to string) string {
	t, _ := time.Parse(day, to)
	return t.AddDate(0, 0, 1).Format(day)
}

// monthEnd is the `due`th of the month after t's, clamped to that month's last day.
func monthEnd(t time.Time, due int) time.Time {
	first := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, ist.Loc)
	return time.Date(first.Year(), first.Month(), min(due, first.AddDate(0, 1, -1).Day()), 0, 0, 0, 0, ist.Loc)
}

func pct(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return r2(part / whole * 100)
}

func add(t *Totals, p Period) {
	t.Interest, t.EMI, t.Extra, t.Principal = r2(t.Interest+p.Interest), r2(t.EMI+p.EMI), r2(t.Extra+p.Extra), r2(t.Principal+p.Principal)
}

// rateOn is the rate in force on a date: the last change at or before it, else the current rate.
func rateOn(e *Emi, date string) float64 {
	rate := *e.Rate
	for _, c := range e.Rates {
		if c.From <= date {
			rate = c.Rate
		}
	}
	return rate
}

// BuildSchedule lays out the statement so far and every period still to come at the current rate. Interest follows the statement:
// balance x rate x days / 365. EMI stays as it is, so a higher rate lengthens the loan rather than raising the payment.
func BuildSchedule(e *Emi, now time.Time) Schedule {
	s := Schedule{Rows: []Row{}, Rates: e.Rates, RateType: e.RateType}
	if e.Kind != "loan" || e.Rate == nil || e.Balance == nil {
		return s
	}
	s.Available, s.Rate = true, *e.Rate
	prevTo := ist.Midnight(now)
	if n := len(e.Ledger); n > 0 {
		prevTo, _ = time.ParseInLocation(day, e.Ledger[n-1].To, ist.Loc)
	} else { // no statement: the period before today's cycle ended the day before the latest due date
		for prevTo.Day() != min(e.DueDay, time.Date(prevTo.Year(), prevTo.Month()+1, 0, 0, 0, 0, 0, ist.Loc).Day()) {
			prevTo = prevTo.AddDate(0, 0, -1)
		}
		prevTo = prevTo.AddDate(0, 0, -1)
	}
	for _, p := range e.Ledger {
		s.Rows = append(s.Rows, Row{Period: p, PaidOn: paidOn(p.To), Rate: rateOn(e, p.From), PrincipalPct: pct(p.Principal, p.EMI+p.Extra), InterestPct: pct(p.Interest, p.EMI+p.Extra)})
		add(&s.Past, p)
	}
	bal := *e.Balance
	for len(s.Rows) < 1200 && bal > 0.5 {
		to := monthEnd(prevTo.AddDate(0, 0, 1), e.DueDay).AddDate(0, 0, -1) // EMIs go out on the due day; the period ends the day before
		rate := rateOn(e, prevTo.AddDate(0, 0, 1).Format(day))
		interest := r2(bal * rate / 100 * to.Sub(prevTo).Hours() / 24 / 365)
		if e.Amount <= interest {
			s.NeverEnds = true
			break
		}
		pay := min(e.Amount, bal+interest)
		bal = r2(bal - (pay - interest))
		p := Period{From: prevTo.AddDate(0, 0, 1).Format(day), To: to.Format(day), Interest: interest, EMI: pay, Principal: r2(pay - interest), Closing: bal}
		s.Rows = append(s.Rows, Row{Period: p, PaidOn: paidOn(p.To), Future: true, Rate: rate, PrincipalPct: pct(p.Principal, pay), InterestPct: pct(interest, pay)})
		add(&s.Ahead, p)
		s.Months++
		s.ClearsOn = p.To
		prevTo = to
	}
	s.Note = "Past rows are your lender's statement. Future rows assume the EMI stays the same and the rate stays at " + strconv.FormatFloat(*e.Rate, 'f', -1, 64) + "%."
	if e.RateType == "variable" {
		s.Note += " This is a variable-rate loan: when the lender resets the rate, update it in Edit and the schedule recalculates."
	}
	return s
}

func (s *Service) Schedule(ctx context.Context, id bson.ObjectID, now time.Time) (Schedule, error) {
	e, err := s.get(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	return BuildSchedule(e, now), nil
}

// OwedOn is the principal still owed on a date (yyyy-mm-dd) across loans that have a statement; ok is false when none covers the date.
func (s *Service) OwedOn(ctx context.Context, date string) (owed float64, ok bool, err error) {
	cur, err := s.col.Find(ctx, bson.M{"ledger.0": bson.M{"$exists": true}})
	if err != nil {
		return 0, false, err
	}
	var all []Emi
	if err := cur.All(ctx, &all); err != nil {
		return 0, false, err
	}
	for _, e := range all {
		if date < e.Ledger[0].From {
			continue
		}
		bal := e.Ledger[0].Closing + e.Ledger[0].Principal // what the loan stood at before the first row
		for _, p := range e.Ledger {
			if p.To <= date {
				bal = p.Closing
			}
		}
		owed, ok = owed+bal, true
	}
	return r2(owed), ok, nil
}
