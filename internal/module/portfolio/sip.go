package portfolio

import (
	"time"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/funds"
)

// graceDays is how long after the SIP date an instalment can still be logged before it counts as missed.
const graceDays = 3

type SipCheck struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
	Day    int     `json:"day"`
	// State is ok | due (date passed, still inside the grace days) | missed | paused.
	State string `json:"state"`
	// LastLogged is the date of the latest logged instalment, empty when none was.
	LastLogged string `json:"lastLogged,omitempty"`
	// StepUp is "up" (raised since a year ago), "none" (not raised in a year), "new" (SIP under a year old) or "" (paused).
	StepUp    string   `json:"stepUp"`
	StepUpPct *float64 `json:"stepUpPct,omitempty"` // amount now against a year ago
}

func daysBetween(a, b time.Time) int { return int(ist.Midnight(a).Sub(ist.Midnight(b)).Hours() / 24) }

// amountAt is the SIP amount on date t: the From of the first change after t, else today's.
func amountAt(s *funds.Sip, t string) float64 {
	for _, c := range s.Changes {
		if c.Date > t {
			return c.From
		}
	}
	return s.Amount
}

// check judges one SIP. since is when it can first have been due (the fund's join date, "" if unknown).
func check(name string, s *funds.Sip, since, lastLogged string, now time.Time) SipCheck {
	c := SipCheck{Name: name, Amount: s.Amount, Day: s.Day, State: "ok", LastLogged: lastLogged}
	if s.Status == "paused" {
		c.State = "paused"
		return c
	}
	// A SIP that began after its latest date was not due yet; otherwise it needs an instalment within the grace of that date.
	due := ist.LastDay(s.Day, now)
	if due.Format("2006-01-02") >= since && lastLogged < due.AddDate(0, 0, -graceDays).Format("2006-01-02") {
		c.State = "missed"
		if daysBetween(now, due) <= graceDays {
			c.State = "due"
		}
	}
	yearAgo := ist.Midnight(now).AddDate(-1, 0, 0).Format("2006-01-02")
	switch then := amountAt(s, yearAgo); {
	case since > yearAgo:
		c.StepUp = "new"
	case s.Amount > then:
		pct := s.Amount/then - 1
		c.StepUp, c.StepUpPct = "up", &pct
	default:
		c.StepUp = "none"
	}
	return c
}

// SipHealth checks every SIP that still exists (live or paused): missed instalments, and whether the amount was raised in the last year.
// "Missed" means no instalment logged in the app, so it is only as good as the logging.
func SipHealth(fs []funds.Fund, genius *funds.GeniusSip, now time.Time) []SipCheck {
	out := []SipCheck{}
	var gLast, gSince string
	for i := range fs {
		f := &fs[i]
		if f.Exited() {
			continue
		}
		last := ""
		for _, in := range f.Instalments {
			last = max(last, in.Date)
		}
		if f.Membership == "Genius" {
			gLast = max(gLast, last)
			if gSince == "" || (f.JoinedAt != "" && f.JoinedAt < gSince) {
				gSince = f.JoinedAt
			}
			continue
		}
		if s := f.OwnSip(); s != nil && s.Status != "cancelled" {
			out = append(out, check(f.Name, s, f.JoinedAt, last, now))
		}
	}
	if genius != nil && genius.Status != "cancelled" {
		out = append(out, check("Genius basket", &genius.Sip, gSince, gLast, now))
	}
	return out
}
