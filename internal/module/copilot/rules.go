package copilot

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"ledger-backend/internal/ist"
)

// Nudge is one thing the app says first. The id is a hash of what it is about, so the same nudge is never stored or sent twice.
type Nudge struct {
	ID           string     `json:"id" bson:"_id"`
	Kind         string     `json:"kind" bson:"kind"`
	Title        string     `json:"title" bson:"title"`
	Body         string     `json:"body" bson:"body"`
	Tone         string     `json:"tone" bson:"tone"` // good | warn | bad | info
	ActionLabel  string     `json:"actionLabel,omitempty" bson:"action_label,omitempty"`
	ActionHref   string     `json:"actionHref,omitempty" bson:"action_href,omitempty"`
	Ref          string     `json:"ref,omitempty" bson:"ref,omitempty"` // the want a reflection is about
	StakePaise   int64      `json:"stakePaise" bson:"stake_paise"`
	State        string     `json:"state" bson:"state"` // new | dismissed | acted
	SnoozedUntil *time.Time `json:"snoozedUntil,omitempty" bson:"snoozed_until,omitempty"`
	CreatedAt    time.Time  `json:"createdAt" bson:"created_at"`
}

// Kinds users can see, mute and be pushed. urgent ones also go to the phone.
var kinds = map[string]struct {
	label  string
	urgent bool
}{
	"pace_today": {"Today's pace", true},
	"pace_cash":  {"Running out before salary", false},
	"weekend":    {"Weekend spending", false},
	"category":   {"Budget pace", false},
	"vendor":     {"Repeat orders", false},
	"commitment": {"Bills short of cash", true},
	"card_due":   {"Card dues", true},
	"price":      {"Price rises", false},
	"renewal":    {"Yearly renewals", false},
	"big_spend":  {"Big purchases", false},
	"under_pace": {"Under pace", false},
	"no_spend":   {"No-spend streaks", false},
	"trigger":    {"Your spending triggers", false},
	"want_ready": {"Parked wants", false},
	"reflect":    {"Purchase check-ins", false},
	"week":       {"Week-ahead brief", false},
}

// exempt from the daily cap: a digest the user expects on a schedule.
const kindWeek = "week"

const maxPerDay = 2

func nudge(kind, key, title, body, tone string, stake int64, label, href string) Nudge {
	h := sha1.Sum([]byte(kind + "|" + key))
	return Nudge{ID: hex.EncodeToString(h[:10]), Kind: kind, Title: title, Body: body, Tone: tone, StakePaise: stake, ActionLabel: label, ActionHref: href, State: "new"}
}

// Rules is every nudge the numbers call for right now, biggest money at stake first. The service removes what was already sent and applies the cap.
func (st *State) Rules() []Nudge {
	var out []Nudge
	add := func(n Nudge) { out = append(out, n) }
	day := st.today().Format("2006-01-02")
	safe := st.SafeNow()
	now := st.Now.In(ist.Loc)

	// Pace
	if safe.TodayPaise > 0 && safe.Ratio >= 0.8 {
		tone, title := "warn", fmt.Sprintf("%.0f%% of today's safe amount spent by %s", safe.Ratio*100, now.Format("3 pm"))
		if safe.Ratio > 1 {
			tone, title = "bad", "Over today's safe amount by "+Rs(-safe.LeftPaise)
		}
		add(nudge("pace_today", day, title, fmt.Sprintf("Pause non-essentials today. Tomorrow's safe amount is %s.", Rs(safe.TomorrowPaise)), tone, safe.SpentPaise, "See today", "/"))
	}
	ev := st.Events(horizon)
	b := st.balances(ev, horizon)
	n := st.nextSalary(0)
	if st.salarySet() {
		for off := 1; off < n; off++ {
			if b[off] < 0 {
				cut := -b[n-1] / int64(n)
				add(nudge("pace_cash", day, fmt.Sprintf("At this pace cash runs out %s before salary", plural(n-off, "day")), "Spending "+Rs(cut)+" less a day fixes it.", "bad", -b[n-1], "Plan it", "/copilot"))
				break
			}
		}
	}
	h := st.Habits()
	if wd := now.Weekday(); (wd == time.Friday || wd == time.Saturday) && h.WeekendRatio >= 1.5 {
		week := st.today().AddDate(0, 0, -int(wd)).Format("2006-01-02")
		cap := st.PaceDaily() * 3 / 2
		add(nudge("weekend", week, fmt.Sprintf("Weekends cost %.1fx a weekday", h.WeekendRatio), "Try a "+Rs(cap)+" cap this weekend.", "info", cap, "", ""))
	}

	// Categories
	if d := now.Day(); d >= 5 {
		days := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, ist.Loc).Day()
		for _, bu := range st.Budgets {
			if proj := bu.Ratio * float64(days) / float64(d); bu.Ratio >= 0.5 && proj >= 1.2 {
				add(nudge("category", bu.Name+"|"+now.Format("2006-01")+"|"+fmt.Sprint(int(proj*4)), fmt.Sprintf("%s: %.0f%% of the budget used on day %d", bu.Name, bu.Ratio*100, d),
					fmt.Sprintf("At this pace you'll finish at %.0f%%.", proj*100), "warn", int64((proj-1)*float64(bu.LimitPaise)), "See budgets", "/budgets"))
			}
		}
	}
	for _, v := range st.Vendors {
		if v.Count >= 3 && v.Count >= 2*v.Last+1 {
			add(nudge("vendor", v.Name+"|"+day, fmt.Sprintf("%s: %s this week (%s)", v.Name, plural(v.Count, "order"), Rs(v.Paise)), fmt.Sprintf("Last week: %d.", v.Last), "info", v.Paise, "", ""))
		}
	}

	// Commitments
	for _, e := range ev {
		if e.Offset < 1 || e.Offset > 14 || e.Paise >= 0 || e.Kind == "sip" {
			continue
		}
		if before := b[e.Offset-1]; before < -e.Paise {
			move := -e.Paise - before
			add(nudge("commitment", e.Name+"|"+e.Date, fmt.Sprintf("%s of %s hits on %s", e.Name, Rs(-e.Paise), st.date(e.Offset).Format("2 Jan")),
				fmt.Sprintf("Your balance the day before is projected at %s. Move %s in.", Rs(before), Rs(move)), "bad", move, "See accounts", "/accounts"))
		}
	}
	for i := range st.Cards {
		c := &st.Cards[i]
		if s := c.Statement; s != nil && s.Remind {
			add(nudge("card_due", c.ID.Hex()+"|"+s.DueDate, fmt.Sprintf("%s due in %s: %s", c.Name, plural(max(s.DaysToDue, 0), "day"), Rs(s.UnpaidPaise)),
				fmt.Sprintf("Paying only the minimum would cost about %s in interest.", Rs(s.InterestIfMinPaise)), "bad", s.UnpaidPaise, "Pay it", "/cards?card="+c.ID.Hex()))
		}
	}

	// Recurring
	for _, r := range st.Recurring {
		if r.Status != "active" {
			continue
		}
		if raised(r) {
			pct := float64(r.LastPaise-r.AmountPaise) / float64(r.AmountPaise) * 100
			add(nudge("price", r.Name+"|"+r.LastAt.Format("2006-01-02"), fmt.Sprintf("%s went up %.0f%%", r.Name, pct),
				fmt.Sprintf("Now %s, usually %s. That is %s more a year.", Rs(r.LastPaise), Rs(r.AmountPaise), Rs((r.LastPaise-r.AmountPaise)*yearly(r.Cadence))), "warn", r.LastPaise-r.AmountPaise, "See recurring", "/insights"))
		}
		if r.Cadence == "yearly" && r.DaysUntil >= 0 && r.DaysUntil <= 7 {
			add(nudge("renewal", r.Name+"|"+r.NextAt.Format("2006-01-02"), fmt.Sprintf("%s renews in %s: %s", r.Name, plural(r.DaysUntil, "day"), Rs(r.AmountPaise)),
				"It is already counted in your safe amount. Cancel now if you no longer need it.", "warn", r.AmountPaise, "See recurring", "/insights"))
		}
	}

	// After the fact: advise only, with a one-tap way to say it was planned.
	if t := st.TopToday; t != nil && t.Paise >= max(safe.TodayPaise*3, 200_000) {
		add(nudge("big_spend", t.ID, fmt.Sprintf("%s at %s", Rs(t.Paise), t.Vendor), fmt.Sprintf("That is %.1f days of runway. Was this planned?", st.RunwayDays(t.Paise)), "info", t.Paise, "", ""))
	}

	// Good news, so it isn't only a nag
	if spent, days := st.weekSoFar(); days >= 3 {
		if under := st.PaceDaily()*int64(days) - spent; under >= 100_000 && safe.Tone == "good" {
			add(nudge("under_pace", st.date(1-days).Format("2006-01-02"), Rs(under)+" under pace this week", fmt.Sprintf("Safe to spend %s on something fun today.", Rs(max(safe.LeftPaise, 0))), "good", under/10, "", ""))
		}
	}
	if h.NoSpendStreak >= 3 {
		add(nudge("no_spend", day, fmt.Sprintf("No-spend streak: %d days", h.NoSpendStreak), fmt.Sprintf("Runway +%.0f days.", st.RunwayDays(st.PaceDaily()*int64(h.NoSpendStreak))), "good", 1, "", ""))
	}

	// Warn before, not after
	tomorrow := st.date(1)
	if h.PriciestDay == tomorrow.Weekday().String() {
		add(nudge("trigger", day, "Tomorrow is your priciest day", fmt.Sprintf("%s usually costs %.1fx an average day. Decide your limit tonight.", h.PriciestDay, h.PriciestRatio), "info", st.PaceDaily(), "", ""))
	}
	if st.salarySet() && h.AfterSalary >= 1.3 && onDay(st.SalaryDay, tomorrow) {
		add(nudge("trigger", "salary|"+day, "Salary lands tomorrow", fmt.Sprintf("The three days after salary usually cost %.1fx other days. Move your savings first.", h.AfterSalary), "info", st.Salary/10, "", ""))
	}

	// Wants
	for i := range st.Wants {
		w := &st.Wants[i]
		switch {
		case w.Status == "parked" && w.RemindAt != nil && !w.RemindAt.After(st.Now):
			c := st.Check(CheckInput{Name: w.Name, AmountPaise: w.AmountPaise, Need: w.Need, Uses: w.Uses})
			add(nudge("want_ready", w.ID.Hex()+"|"+w.RemindAt.Format("2006-01-02"), "Still want "+w.Name+"?", fmt.Sprintf("%s for %s.", c.Headline, Rs(w.AmountPaise)), "info", w.AmountPaise, "See wants", "/copilot"))
		case w.Status == "bought" && w.Glad == nil && w.BoughtAt != nil && st.Now.Sub(*w.BoughtAt) >= 7*24*time.Hour:
			r := nudge("reflect", w.ID.Hex(), "Still glad you bought "+w.Name+"?", "Your answer shapes future advice.", "info", w.AmountPaise/10, "", "")
			r.Ref = w.ID.Hex()
			add(r)
		}
	}

	// The brief, on Mondays
	if now.Weekday() == time.Monday {
		w := st.WeekAhead(7)
		body := w.Text(st)
		if rw := st.Runway(); rw.RecapText != "" {
			body += " " + rw.RecapText
		}
		add(nudge(kindWeek, day, "Your week ahead", body, "info", 0, "Open", "/copilot"))
	}

	slices.SortStableFunc(out, func(a, b Nudge) int {
		switch {
		case a.StakePaise > b.StakePaise:
			return -1
		case a.StakePaise < b.StakePaise:
			return 1
		}
		return 0
	})
	return out
}

func yearly(cadence string) int64 {
	switch cadence {
	case "weekly":
		return 52
	case "monthly":
		return 12
	case "quarterly":
		return 4
	}
	return 1
}

// pick applies the rules of silence: nothing muted, nothing already sent, and at most `room` from the capped kinds. The brief always goes.
func pick(all []Nudge, seen, muted map[string]bool, room int) []Nudge {
	var out []Nudge
	for _, n := range all {
		if seen[n.ID] || muted[n.Kind] {
			continue
		}
		if n.Kind != kindWeek {
			if room <= 0 {
				continue
			}
			room--
		}
		out = append(out, n)
	}
	return out
}
