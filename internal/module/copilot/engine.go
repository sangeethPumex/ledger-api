// Package copilot turns what the app already knows into "what should I do now": safe to spend, can I buy this, what if I wait, the week ahead
// and nudges. Everything in this file is pure arithmetic on a State, so every verdict is explainable and testable. All money is paise.
package copilot

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/budgets"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/insights"
	"ledger-backend/internal/module/mandates"
)

const (
	horizon        = 482 // days the balance is projected: a 90-day search for "when can I buy this", the longest mode (a year), and one pay period
	waitSearchDays = 90  // how far ahead a "wait" is looked for
	skipWantDays   = 60  // a want that cannot pass within this long is a skip, not a wait
	coolingDays    = 14
	emiRate        = 16.0 // % a year assumed for the EMI comparison, with emiFee
	emiFee         = 0.01
	emiMonths      = 6
)

// Dated is a monthly payment on a day of the month.
type Dated struct {
	Name  string
	Day   int
	Paise int64
}

type Renewal struct {
	Name  string
	Paise int64
	Days  int
}

// DayRow is one finished IST day from the snapshot table.
type DayRow struct {
	Date   string
	Spent  int64
	Income int64
}

// VendorWeek is how often a vendor was paid this week and last week.
type VendorWeek struct {
	Name        string
	Count, Last int
	Paise       int64
}

// BudgetRow is a budget with its category's name.
type BudgetRow struct {
	budgets.Status
	Name string
}

// State is every input the engine reads. The service fills it from the other modules.
type State struct {
	Now               time.Time
	Mode              string // key of the chosen Mode
	Cash, Buffer      int64
	Salary            int64
	SalaryDay         int
	Fixed, EmiMonthly int64 // per month
	SipMonthly        int64
	Emis, Sips        []Dated
	Mandates          []mandates.Occ
	Renewals          []Renewal // yearly or quarterly charges detected but not tracked as mandates
	Cards             []cards.View
	Budgets           []BudgetRow
	Recurring         []insights.Recurring
	Days              []DayRow // finished days, oldest first, at most 90
	TodaySpent        int64    // all of today's spending
	TodayCard         int64    // the part on cards, which has not left cash yet
	TopToday          *TopTxn
	Owed              int64
	Vendors           []VendorWeek
	Wants             []Want
	AccountsTotal     int
	AccountsMoved     int // accounts whose balance changed in the last 30 days
}

type TopTxn struct {
	ID     string
	Vendor string
	Paise  int64
}

// Rs is ₹1,23,456 style, rounded to the rupee.
func Rs(paise int64) string {
	neg := paise < 0
	if neg {
		paise = -paise
	}
	s := strconv.FormatInt((paise+50)/100, 10)
	if len(s) > 3 {
		head, tail := s[:len(s)-3], s[len(s)-3:]
		var parts []string
		for len(head) > 2 {
			parts = append([]string{head[len(head)-2:]}, parts...)
			head = head[:len(head)-2]
		}
		s = strings.Join(append([]string{head}, parts...), ",") + "," + tail
	}
	if neg {
		return "-₹" + s
	}
	return "₹" + s
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func (st *State) today() time.Time       { return ist.Midnight(st.Now) }
func (st *State) date(off int) time.Time { return st.today().AddDate(0, 0, off) }

func onDay(day int, d time.Time) bool {
	last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, ist.Loc).Day()
	return d.Day() == min(day, last)
}

// DaysToSalary falls back to 30 when no salary is set, the same horizon the dashboard uses.
func (st *State) DaysToSalary() int {
	if st.Salary > 0 && st.SalaryDay > 0 {
		return ist.DaysUntil(st.SalaryDay, st.Now)
	}
	return 30
}

// nextSalary is the first pay day strictly after offset k, as an offset.
func (st *State) nextSalary(k int) int {
	if st.Salary <= 0 || st.SalaryDay <= 0 {
		return k + 30
	}
	for off := k + 1; off <= k+32; off++ {
		if onDay(st.SalaryDay, st.date(off)) {
			return off
		}
	}
	return k + 30
}

// PaceDaily is the usual day's spending: the mean of the last 30 finished days without the largest tenth, so a rent payment or an EMI day
// (which the timeline already counts) does not inflate every future day.
func (st *State) PaceDaily() int64 {
	rows := st.Days[max(len(st.Days)-30, 0):]
	if len(rows) == 0 {
		return 0
	}
	v := make([]int64, len(rows))
	for i, r := range rows {
		v[i] = r.Spent
	}
	slices.Sort(v)
	v = v[:len(v)-int(math.Ceil(float64(len(v))/10))]
	if len(v) == 0 {
		return 0
	}
	var sum int64
	for _, x := range v {
		sum += x
	}
	return sum / int64(len(v))
}

// BurnDaily is what a day of runway costs: the dashboard's "if income stopped" burn (average spending, fixed costs and EMIs), per day.
func (st *State) BurnDaily() float64 {
	var sum int64
	for _, r := range st.Days[max(len(st.Days)-30, 0):] {
		sum += r.Spent
	}
	return float64(sum+st.Fixed+st.EmiMonthly) / 30
}

// RunwayDays is how many days paise lasts at BurnDaily; 0 when nothing is being spent.
func (st *State) RunwayDays(paise int64) float64 {
	b := st.BurnDaily()
	if b <= 0 {
		return 0
	}
	return math.Max(float64(paise), 0) / b
}

// Event is one known movement of money, offset days from today.
type Event struct {
	Offset int    `json:"offset"`
	Date   string `json:"date"`
	Name   string `json:"name"`
	Paise  int64  `json:"paise"`           // negative going out
	Kind   string `json:"kind"`            // salary | mandate | emi | sip | card | renewal
	Count  int    `json:"count,omitempty"` // how many payments Paise adds up, when grouped
}

// Events lists every known movement in the next h days, soonest first. A bill past its due date counts as today.
// ponytail: an EMI charged to a card can count twice (as an EMI and on the card's bill); it errs on the cautious side. Pass the card id through emis.Brief if that bites.
func (st *State) Events(h int) []Event {
	var out []Event
	add := func(off int, name string, paise int64, kind string) {
		if off <= h && paise != 0 {
			out = append(out, Event{Offset: off, Date: st.date(off).Format("2006-01-02"), Name: name, Paise: paise, Kind: kind})
		}
	}
	for off := 1; off <= h; off++ {
		d := st.date(off)
		if st.Salary > 0 && st.SalaryDay > 0 && onDay(st.SalaryDay, d) {
			add(off, "Salary", st.Salary, "salary")
		}
		for _, e := range st.Emis {
			if onDay(e.Day, d) {
				add(off, e.Name, -e.Paise, "emi")
			}
		}
		for _, s := range st.Sips {
			if onDay(s.Day, d) {
				add(off, s.Name, -s.Paise, "sip")
			}
		}
	}
	for _, m := range st.Mandates {
		add(m.Days, m.Name, -m.Paise, "mandate")
	}
	for _, r := range st.Renewals {
		add(r.Days, r.Name, -r.Paise, "renewal")
	}
	for i := range st.Cards {
		c := &st.Cards[i]
		if s := c.Statement; s != nil && s.UnpaidPaise > 0 {
			add(max(s.DaysToDue, 0), c.Name+" bill", -s.UnpaidPaise, "card")
		}
		if due, ok := nextBillDue(c); ok {
			add(due, c.Name+" next bill", -c.CycleSpentPaise, "card")
		}
	}
	slices.SortStableFunc(out, func(a, b Event) int { return a.Offset - b.Offset })
	return out
}

// nextBillDue is when the running cycle's bill falls due, in days. The gap between a statement and its due date is under a month on every card.
func nextBillDue(c *cards.View) (int, bool) {
	if c.DaysToBilling == nil || c.DaysToDue == nil {
		return 0, false
	}
	gap := *c.DaysToDue - *c.DaysToBilling
	if gap <= 0 {
		gap += 30
	}
	return *c.DaysToBilling + gap, true
}

// balances projects the closing balance of each day: known events, plus a usual day's spending from tomorrow.
func (st *State) balances(ev []Event, h int) []int64 {
	b := make([]int64, h+1)
	pace := st.PaceDaily()
	for off := range b {
		if off == 0 {
			b[0] = st.Cash
		} else {
			b[off] = b[off-1] - pace
		}
		for _, e := range ev {
			if e.Offset == off {
				b[off] += e.Paise
			}
		}
	}
	return b
}

// Mode is how hard the money has to last. Months is the goal: cash should carry everything for that long with no salary.
// FloorPct is a share of the usual day's spending, the least a day can be budgeted while that goal is out of reach.
type Mode struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Months   int    `json:"months"`
	FloorPct int64  `json:"floorPct"`
}

const DefaultMode = "normal"

var Modes = []Mode{
	{"ultra_poor", "Ultra poor", 12, 30},
	{"saving", "Saving", 6, 50},
	{DefaultMode, "Normal", 3, 80},
	{"luxury", "Luxury", 1, 100},
}

// ValidMode reports whether key names a mode.
func ValidMode(key string) bool {
	for _, m := range Modes {
		if m.Key == key {
			return true
		}
	}
	return false
}

func (st *State) mode() Mode {
	for _, m := range Modes {
		if m.Key == st.Mode {
			return m
		}
	}
	return Modes[2]
}

// budget splits cash over the mode's months. after is the offset the cash is measured at: only what falls due later is taken off.
// SIPs are left out because they pause when the salary stops. goal is the daily figure that truly reaches the target; floor is the mode's minimum.
func (st *State) budget(cash int64, after int, ev []Event) (goal, floor, committed int64) {
	m := st.mode()
	days := m.Months * 30
	for _, e := range ev {
		if e.Offset > after && e.Offset <= after+days && e.Paise < 0 && e.Kind != "sip" {
			committed -= e.Paise
		}
	}
	committed += st.Fixed * int64(m.Months)
	return max(cash-committed-st.Buffer, 0) / int64(days), st.PaceDaily() * m.FloorPct / 100, committed
}

// Safe is the core number: what today can spend while cash keeps the mode's promise of months without a salary.
type Safe struct {
	TodayPaise     int64   `json:"todayPaise"`
	SpentPaise     int64   `json:"spentPaise"`
	LeftPaise      int64   `json:"leftPaise"`
	TomorrowPaise  int64   `json:"tomorrowPaise"` // today's under- or overspend carried over
	Ratio          float64 `json:"ratio"`
	Tone           string  `json:"tone"` // good under 70%, warn to 100%, bad over
	CashPaise      int64   `json:"cashPaise"`
	CommittedPaise int64   `json:"committedPaise"` // everything due over the mode's months, fixed costs included
	BufferPaise    int64   `json:"bufferPaise"`
	Committed      []Event `json:"committed"`  // grouped by name, with a count
	FixedPaise     int64   `json:"fixedPaise"` // the fixed-cost part of CommittedPaise
	Mode           Mode    `json:"mode"`
	Modes          []Mode  `json:"modes"`
	GoalPaise      int64   `json:"goalPaise"`   // the daily figure that really reaches the goal
	OnFloor        bool    `json:"onFloor"`     // the goal is out of reach, so today is the mode's minimum
	ShortPaise     int64   `json:"shortPaise"`  // the cash missing to reach the goal at the mode's minimum
	SustainDays    float64 `json:"sustainDays"` // how long cash lasts at this budget with no salary
}

// SafeNow divides what is free by the mode's days. Today's bank spending is added back so spending does not shrink today's own allowance;
// tomorrow's figure takes all of it off, which is what carries an under- or overspend forward.
func (st *State) SafeNow() Safe {
	m := st.mode()
	days := m.Months * 30
	ev := st.Events(days)
	start := st.Cash + (st.TodaySpent - st.TodayCard)
	goal, floor, committed := st.budget(start, -1, ev)
	s := Safe{SpentPaise: st.TodaySpent, CashPaise: st.Cash, BufferPaise: st.Buffer, CommittedPaise: committed, FixedPaise: st.Fixed * int64(m.Months), Mode: m, Modes: Modes, GoalPaise: goal, Committed: []Event{}, TodayPaise: max(goal, floor), OnFloor: floor > goal}
	at := map[string]int{}
	for _, e := range ev {
		if e.Paise >= 0 || e.Kind == "sip" {
			continue
		}
		if i, ok := at[e.Name]; ok {
			s.Committed[i].Paise += e.Paise
			s.Committed[i].Count++
			continue
		}
		e.Count = 1
		at[e.Name] = len(s.Committed)
		s.Committed = append(s.Committed, e)
	}
	if s.OnFloor {
		s.ShortPaise = floor*int64(days) + committed + st.Buffer - start
	}
	if perDay := float64(s.TodayPaise) + float64(committed)/float64(days); perDay > 0 {
		s.SustainDays = math.Max(float64(start-st.Buffer), 0) / perDay
	}
	s.LeftPaise = s.TodayPaise - st.TodaySpent
	nextGoal := max(start-st.TodaySpent-committed-st.Buffer, 0) / int64(max(days-1, 1))
	s.TomorrowPaise = max(nextGoal, floor)
	if s.TodayPaise > 0 {
		s.Ratio = float64(st.TodaySpent) / float64(s.TodayPaise)
	} else if st.TodaySpent > 0 {
		s.Ratio = 2
	}
	switch {
	case s.TodayPaise == 0 || s.Ratio > 1:
		s.Tone = "bad"
	case s.Ratio >= 0.7:
		s.Tone = "warn"
	default:
		s.Tone = "good"
	}
	return s
}

// CheckInput is a purchase being considered. Date (YYYY-MM-DD, optional) asks about one specific day.
type CheckInput struct {
	Name          string `json:"name"`
	AmountPaise   int64  `json:"amountPaise"`
	Need          bool   `json:"need"`
	Uses          int    `json:"uses"`          // how many times it will be used
	ReplacesPaise int64  `json:"replacesPaise"` // a monthly cost it would end
	OnCard        bool   `json:"onCard"`
	Date          string `json:"date"`
}

const maxAmount = 10_000_000_000 // 10 crore rupees, the sanity cap used across the app

func (in *CheckInput) Validate() string {
	switch {
	case in.AmountPaise <= 0 || in.AmountPaise > maxAmount:
		return "amount must be above zero"
	case len(in.Name) > 120:
		return "name is too long"
	case in.Uses < 0 || in.ReplacesPaise < 0 || in.ReplacesPaise > maxAmount:
		return "uses and replaced cost cannot be negative"
	}
	if in.Date != "" {
		if _, err := time.ParseInLocation("2006-01-02", in.Date, ist.Loc); err != nil {
			return "date must be YYYY-MM-DD"
		}
	}
	return ""
}

// Scenario is the purchase on one day.
type Scenario struct {
	Label         string  `json:"label"`
	Date          string  `json:"date"`
	RunwayAfter   float64 `json:"runwayAfter"`
	SafePerDay    int64   `json:"safePerDayPaise"`
	Uncovered     string  `json:"uncovered,omitempty"` // the first commitment the purchase leaves short
	InterestPaise int64   `json:"interestPaise"`       // a month's interest if on a card and the bill cannot be paid in full
	ShortPaise    int64   `json:"shortPaise"`          // how far the purchase leaves the mode's goal out of reach
	Passes        bool    `json:"passes"`
}

// evaluate buys at offset k and looks at the pay period that follows. On a card the money leaves when the bill falls due.
func (st *State) evaluate(ev []Event, b []int64, k int, in CheckInput) Scenario {
	pay := k
	var card *cards.View
	if in.OnCard {
		if card = st.bestCard(in.AmountPaise); card != nil {
			pay = min(k+freeDays(card), horizon)
		}
	}
	bought := func(off int) int64 {
		if off >= pay {
			return b[off] - in.AmountPaise
		}
		return b[off]
	}
	n := min(st.nextSalary(k), horizon)
	s := Scenario{Date: st.date(k).Format("2006-01-02"), RunwayAfter: st.RunwayDays(b[k] - in.AmountPaise), Passes: true}
	// A card purchase is still money owed, so the goal is measured with it taken off whenever it gets paid.
	goal, floor, committed := st.budget(b[k]-in.AmountPaise, k, ev)
	before, _, _ := st.budget(b[k], k, ev)
	s.SafePerDay = max(goal, floor)
	// Spending that fits a day's budget is what the budget is for; anything larger must leave the goal within reach.
	if goal < floor && in.AmountPaise > max(before, floor) {
		s.Passes = false
		s.ShortPaise = floor*int64(st.mode().Months*30) + committed + st.Buffer - (b[k] - in.AmountPaise)
	}
	for off := k; off < n; off++ {
		if bal := bought(off); bal < st.Buffer {
			s.Passes = false
			if bal < 0 && s.Uncovered == "" {
				s.Uncovered = "your spending"
				for _, e := range ev {
					if e.Offset == off && e.Paise < 0 {
						s.Uncovered = e.Name
						break
					}
				}
			}
		}
	}
	if card != nil && bought(pay) < 0 {
		apr := card.Apr
		if apr <= 0 {
			apr = cards.DefaultApr
		}
		s.InterestPaise = int64(float64(in.AmountPaise) * apr / 100 / 12)
	}
	return s
}

// EmiCompare is what spreading the purchase over instalments would add.
type EmiCompare struct {
	Months     int     `json:"months"`
	RatePct    float64 `json:"ratePct"`
	FeePct     float64 `json:"feePct"`
	ExtraPaise int64   `json:"extraPaise"`
	Monthly    int64   `json:"monthlyPaise"`
}

func emiCost(p int64) EmiCompare {
	r := emiRate / 100 / 12
	f := math.Pow(1+r, emiMonths)
	monthly := float64(p) * r * f / (f - 1)
	return EmiCompare{emiMonths, emiRate, emiFee * 100, int64(monthly*emiMonths) - p + int64(float64(p)*emiFee), int64(monthly)}
}

type CheckResult struct {
	Name          string     `json:"name,omitempty"`
	AmountPaise   int64      `json:"amountPaise"`
	Verdict       string     `json:"verdict"` // buy | wait | skip
	Headline      string     `json:"headline"`
	RunwayBefore  float64    `json:"runwayBefore"`
	RunwayAfter   float64    `json:"runwayAfter"`
	RunwayCost    float64    `json:"runwayCost"`
	SafeBefore    int64      `json:"safeBeforePaise"`
	SafeAfter     int64      `json:"safeAfterPaise"`
	PlanCost      string     `json:"planCost"`
	Earliest      string     `json:"earliest,omitempty"` // first day it passes, when that is not today
	Reasons       []string   `json:"reasons"`            // the maths, so the user can disagree
	Scenarios     []Scenario `json:"scenarios"`
	CoolOff       string     `json:"coolOff"` // the day to ask again
	SavePerDay    int64      `json:"savePerDayPaise"`
	SaveBy        string     `json:"saveBy"`
	CostPerUse    int64      `json:"costPerUsePaise,omitempty"`
	PaybackMonths float64    `json:"paybackMonths,omitempty"`
	Emi           EmiCompare `json:"emi"`
}

// Check answers "can I buy this?" and always in the same shape. Buy: nothing is left short and safe-to-spend stays above the floor.
// Wait: it fails only on timing, and Earliest says when it passes. Skip: it does not pass in the next 90 days (60 for a want).
func (st *State) Check(in CheckInput) CheckResult {
	ev := st.Events(horizon)
	b := st.balances(ev, horizon)
	now := st.evaluate(ev, b, 0, in)
	base := st.evaluate(ev, b, 0, CheckInput{})
	r := CheckResult{
		Name: in.Name, AmountPaise: in.AmountPaise,
		RunwayBefore: st.RunwayDays(st.Cash), RunwayAfter: now.RunwayAfter, SafeBefore: base.SafePerDay, SafeAfter: now.SafePerDay,
		CoolOff: st.date(coolingDays).Format("2006-01-02"), Emi: emiCost(in.AmountPaise), Reasons: []string{},
	}
	r.RunwayCost = r.RunwayBefore - r.RunwayAfter

	earliest := -1
	start := 0
	if in.Date != "" {
		if d, err := time.ParseInLocation("2006-01-02", in.Date, ist.Loc); err == nil {
			start = max(min(int(d.Sub(st.today()).Hours()/24), waitSearchDays), 0)
		}
	}
	for k := start; k <= waitSearchDays; k++ {
		if st.evaluate(ev, b, k, in).Passes {
			earliest = k
			break
		}
	}
	switch {
	case earliest == start:
		r.Verdict, r.Headline = "buy", "Buy now"
		if start > 0 {
			r.Headline = "Fine on " + st.date(start).Format("2 Jan")
		}
	case earliest < 0 || (!in.Need && earliest > skipWantDays):
		r.Verdict, r.Headline = "skip", "Skip or rethink"
	default:
		r.Verdict, r.Earliest = "wait", st.date(earliest).Format("2006-01-02")
		r.Headline = "Wait until " + st.date(earliest).Format("2 Jan")
	}

	r.PlanCost = st.planCost(in.AmountPaise)
	target := earliest
	if target <= 0 {
		target = 30
	}
	r.SavePerDay, r.SaveBy = in.AmountPaise/int64(target), st.date(target).Format("2006-01-02")

	if now.Uncovered != "" {
		r.Reasons = append(r.Reasons, "Buying today leaves "+now.Uncovered+" short before your next salary.")
	}
	if now.ShortPaise > 0 {
		m := st.mode()
		r.Reasons = append(r.Reasons, fmt.Sprintf("%s mode aims for %s without salary. Today this purchase leaves that %s short.", m.Label, plural(m.Months, "month"), Rs(now.ShortPaise)))
	}
	if st.Buffer > 0 && st.Cash-in.AmountPaise < st.Buffer {
		r.Reasons = append(r.Reasons, "Cash would fall below your "+Rs(st.Buffer)+" buffer.")
	}
	if st.Owed >= in.AmountPaise/2 && st.Owed > 0 {
		r.Reasons = append(r.Reasons, "Friends owe you "+Rs(st.Owed)+". Chasing that first covers a good part of this.")
	}
	if !in.Need {
		if bad, all := st.regrets(); all >= 2 && bad*2 >= all {
			r.Reasons = append(r.Reasons, fmt.Sprintf("You later regretted %d of your last %d wants, so give this one a few days.", bad, all))
		}
	}
	if !st.salarySet() {
		r.Reasons = append(r.Reasons, "No salary is set, so future income is not counted. Set it in About Me for a fairer answer.")
	}
	if in.Uses > 0 {
		r.CostPerUse = in.AmountPaise / int64(in.Uses)
	}
	if in.ReplacesPaise > 0 {
		r.PaybackMonths = float64(in.AmountPaise) / float64(in.ReplacesPaise)
	}

	salary := st.nextSalary(0)
	for _, c := range []struct {
		label string
		k     int
	}{{"Now", 0}, {"In 2 weeks", 14}, {"In 1 month", 30}, {"After salary", salary}} {
		s := st.evaluate(ev, b, c.k, in)
		s.Label = c.label
		r.Scenarios = append(r.Scenarios, s)
	}
	return r
}

func (st *State) salarySet() bool { return st.Salary > 0 && st.SalaryDay > 0 }

// planCost is what the purchase does to the emergency-fund goal, or else how long saving takes to replace it.
func (st *State) planCost(amount int64) string {
	saved := (st.Salary - st.PaceDaily()*30 - st.Fixed - st.EmiMonthly - st.SipMonthly) / 30
	if saved <= 0 {
		return "You are not saving at this pace, so nothing refills this."
	}
	days := int(amount / saved)
	if st.Buffer > 0 && st.Cash < st.Buffer+amount {
		return "Pushes your emergency fund back " + plural(days, "day") + "."
	}
	return "Takes " + plural(days, "day") + " of saving to replace."
}

// Runway is everything in days, because days feel real.
type Runway struct {
	Days      float64 `json:"days"`
	BurnDaily int64   `json:"burnDailyPaise"`
	ZeroDate  string  `json:"zeroDate,omitempty"` // the day cash runs out if income stopped
	GoalDays  float64 `json:"goalDays"`           // the emergency fund, in the same unit
	RecapDays float64 `json:"recapDays"`          // this week against a usual week; positive means days bought
	RecapText string  `json:"recapText,omitempty"`
}

func (st *State) Runway() Runway {
	d := st.RunwayDays(st.Cash)
	r := Runway{Days: d, BurnDaily: int64(st.BurnDaily()), GoalDays: st.RunwayDays(st.Buffer)}
	if d > 0 && d < 3650 {
		r.ZeroDate = st.date(int(d)).Format("2006-01-02")
	}
	if spent, days := st.weekSoFar(); days >= 3 && st.BurnDaily() > 0 {
		gap := float64(st.PaceDaily()*int64(days)-spent) / st.BurnDaily()
		r.RecapDays = gap
		switch {
		case gap >= 0.5:
			r.RecapText = fmt.Sprintf("You bought %.1f days of freedom this week by spending under your usual pace.", gap)
		case gap <= -0.5:
			r.RecapText = fmt.Sprintf("This week cost %.1f days of runway over your usual pace.", -gap)
		}
	}
	return r
}

// weekSoFar is spending since Monday, today included, and how many days that is.
func (st *State) weekSoFar() (int64, int) {
	wd := (int(st.today().Weekday()) + 6) % 7 // Monday = 0
	spent := st.TodaySpent
	from := st.date(-wd).Format("2006-01-02")
	for _, r := range st.Days {
		if r.Date >= from {
			spent += r.Spent
		}
	}
	return spent, wd + 1
}

// Week is the week-ahead brief.
type Week struct {
	Days        int     `json:"days"`
	InPaise     int64   `json:"inPaise"`
	OutPaise    int64   `json:"outPaise"`
	Events      []Event `json:"events"`
	LowestPaise int64   `json:"lowestPaise"`
	LowestDate  string  `json:"lowestDate"`
	AboveBuffer bool    `json:"aboveBuffer"`
	SafePerDay  int64   `json:"safePerDayPaise"`
	Priciest    string  `json:"priciest,omitempty"` // the weekday that usually costs most
}

func (st *State) WeekAhead(days int) Week {
	ev := st.Events(days)
	b := st.balances(ev, days)
	w := Week{Days: days, Events: []Event{}, SafePerDay: st.SafeNow().TodayPaise, LowestPaise: b[0], LowestDate: st.date(0).Format("2006-01-02")}
	for _, e := range ev {
		if e.Paise > 0 {
			w.InPaise += e.Paise
		} else {
			w.OutPaise -= e.Paise
		}
		w.Events = append(w.Events, e)
	}
	for off, bal := range b {
		if bal < w.LowestPaise {
			w.LowestPaise, w.LowestDate = bal, st.date(off).Format("2006-01-02")
		}
	}
	w.AboveBuffer = w.LowestPaise >= st.Buffer
	if h := st.Habits(); h.PriciestDay != "" {
		w.Priciest = h.PriciestDay
	}
	return w
}

func (w Week) Text(st *State) string {
	var out []string
	for i, e := range w.Events {
		if e.Paise < 0 && i < 4 {
			out = append(out, fmt.Sprintf("%s %s", e.Name, st.date(e.Offset).Format("2 Jan")))
		}
	}
	s := fmt.Sprintf("In %s, out %s", Rs(w.InPaise), Rs(w.OutPaise))
	if len(out) > 0 {
		s += " (" + strings.Join(out, ", ") + ")"
	}
	lowDate, _ := time.ParseInLocation("2006-01-02", w.LowestDate, ist.Loc)
	s += fmt.Sprintf(". Lowest balance %s on %s", Rs(w.LowestPaise), lowDate.Format("Mon 2 Jan"))
	if w.AboveBuffer {
		s += ", above buffer."
	} else {
		s += ", below your buffer."
	}
	s += fmt.Sprintf(" Safe to spend %s a day.", Rs(w.SafePerDay))
	if w.Priciest != "" {
		s += " " + w.Priciest + " is usually your priciest day."
	}
	return s
}

// Habits is what the history says about behaviour.
type Habits struct {
	NoSpendStreak int      `json:"noSpendStreak"`
	UnderStreak   int      `json:"underStreak"`
	PriciestDay   string   `json:"priciestDay,omitempty"`
	PriciestRatio float64  `json:"priciestRatio,omitempty"`
	WeekendRatio  float64  `json:"weekendRatio"`
	AfterSalary   float64  `json:"afterSalaryRatio"` // the three days after salary against other days
	Notes         []string `json:"notes"`
}

// Habits needs four weeks of history before it claims a pattern.
// ponytail: the under-safe streak measures past days against today's safe figure, not the one they had. Store a daily figure if it needs to be exact.
func (st *State) Habits() Habits {
	h := Habits{Notes: []string{}}
	safe := st.SafeNow().TodayPaise
	noSpend, under := true, true
	for i := len(st.Days) - 1; i >= 0; i-- {
		s := st.Days[i].Spent
		if noSpend = noSpend && s == 0; noSpend {
			h.NoSpendStreak++
		}
		if under = under && safe > 0 && s <= safe; under {
			h.UnderStreak++
		}
	}
	if len(st.Days) < 28 {
		return h
	}
	var sum [7]int64
	var cnt [7]int64
	var total, wkend, wkday, post, other int64
	var nWkend, nWkday, nPost, nOther int64
	for _, r := range st.Days {
		d, err := time.ParseInLocation("2006-01-02", r.Date, ist.Loc)
		if err != nil {
			continue
		}
		wd := int(d.Weekday())
		sum[wd] += r.Spent
		cnt[wd]++
		total += r.Spent
		if wd == 0 || wd == 6 {
			wkend, nWkend = wkend+r.Spent, nWkend+1
		} else {
			wkday, nWkday = wkday+r.Spent, nWkday+1
		}
		isPost := false
		if st.salarySet() {
			for back := 1; back <= 3; back++ {
				if onDay(st.SalaryDay, d.AddDate(0, 0, -back)) {
					isPost = true
				}
			}
		}
		if isPost {
			post, nPost = post+r.Spent, nPost+1
		} else {
			other, nOther = other+r.Spent, nOther+1
		}
	}
	avg := float64(total) / float64(len(st.Days))
	if avg <= 0 {
		return h
	}
	best := 0
	for wd := range sum {
		if cnt[wd] > 0 && cnt[best] > 0 && float64(sum[wd])/float64(cnt[wd]) > float64(sum[best])/float64(cnt[best]) {
			best = wd
		}
	}
	if cnt[best] > 0 {
		if ratio := float64(sum[best]) / float64(cnt[best]) / avg; ratio >= 1.3 {
			h.PriciestDay, h.PriciestRatio = time.Weekday(best).String(), ratio
			h.Notes = append(h.Notes, fmt.Sprintf("%s is your priciest day: %.1fx an average day.", h.PriciestDay, ratio))
		}
	}
	if nWkend > 0 && nWkday > 0 && wkday > 0 {
		h.WeekendRatio = (float64(wkend) / float64(nWkend)) / (float64(wkday) / float64(nWkday))
		if h.WeekendRatio >= 1.5 {
			h.Notes = append(h.Notes, fmt.Sprintf("Weekends cost %.1fx a weekday.", h.WeekendRatio))
		}
	}
	if nPost > 0 && nOther > 0 && other > 0 {
		h.AfterSalary = (float64(post) / float64(nPost)) / (float64(other) / float64(nOther))
		if h.AfterSalary >= 1.3 {
			h.Notes = append(h.Notes, fmt.Sprintf("The three days after salary cost %.1fx other days.", h.AfterSalary))
		}
	}
	return h
}

// regrets counts the bought wants the user has answered about.
func (st *State) regrets() (bad, all int) {
	for _, w := range st.Wants {
		if w.Glad != nil {
			all++
			if !*w.Glad {
				bad++
			}
		}
	}
	return bad, all
}

// Advice is one piece of card guidance.
type Advice struct {
	Kind       string `json:"kind"` // timing | utilisation | payoff
	Card       string `json:"card"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	StakePaise int64  `json:"stakePaise"`
}

func freeDays(c *cards.View) int {
	due, ok := nextBillDue(c)
	if !ok {
		return 0
	}
	return due
}

// bestCard is the card that gives the longest interest-free time and can take the amount; nil without one.
func (st *State) bestCard(amount int64) *cards.View {
	var best *cards.View
	for i := range st.Cards {
		c := &st.Cards[i]
		if _, ok := nextBillDue(c); !ok || (c.AvailablePaise != nil && *c.AvailablePaise < amount) {
			continue
		}
		if best == nil || freeDays(c) > freeDays(best) {
			best = c
		}
	}
	return best
}

const utilTarget = 0.3

// CardAdvice is which card to use now, what to pay before a statement, and what paying only the minimum would cost.
func (st *State) CardAdvice() []Advice {
	out := []Advice{}
	if best := st.bestCard(1); best != nil {
		worst := best
		for i := range st.Cards {
			c := &st.Cards[i]
			if _, ok := nextBillDue(c); ok && freeDays(c) < freeDays(worst) {
				worst = c
			}
		}
		if worst != best && freeDays(best)-freeDays(worst) >= 7 {
			out = append(out, Advice{Kind: "timing", Card: best.Name, Title: "Use " + best.Name + " for the next " + plural(*best.DaysToBilling, "day"),
				Body: fmt.Sprintf("Its statement date is the %s, so a purchase today gets about %d days interest-free, against %d on %s.", ordinal(best.BillingDay), freeDays(best), freeDays(worst), worst.Name)})
		}
	}
	for i := range st.Cards {
		c := &st.Cards[i]
		if c.SharedWith == nil && c.LimitPaise > 0 && c.Utilisation != nil && *c.Utilisation > utilTarget && c.DaysToBilling != nil {
			used := c.UsedPaise
			if c.GroupUsedPaise > 0 {
				used = c.GroupUsedPaise
			}
			pay := used - int64(utilTarget*float64(c.LimitPaise))
			out = append(out, Advice{Kind: "utilisation", Card: c.Name, Title: fmt.Sprintf("%s is at %.0f%% utilisation", c.Name, *c.Utilisation*100),
				Body: fmt.Sprintf("Paying %s before the statement date in %s brings it under 30%%.", Rs(pay), plural(*c.DaysToBilling, "day")), StakePaise: pay})
		}
		if s := c.Statement; s != nil && s.UnpaidPaise > 0 {
			out = append(out, Advice{Kind: "payoff", Card: c.Name, Title: "Pay " + c.Name + " in full: " + Rs(s.UnpaidPaise),
				Body: fmt.Sprintf("Paying only the %s minimum would cost about %s in interest next month. Skipping it costs about %s.", Rs(s.MinDuePaise), Rs(s.InterestIfMinPaise), Rs(s.InterestIfSkippedPaise)), StakePaise: s.InterestIfMinPaise})
		}
	}
	return out
}

func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// Subscription is a recurring charge in runway terms.
type Subscription struct {
	Name        string  `json:"name"`
	Cadence     string  `json:"cadence"`
	AmountPaise int64   `json:"amountPaise"`
	YearlyPaise int64   `json:"yearlyPaise"`
	RunwayMonth float64 `json:"runwayDaysPerMonth"`    // what cancelling gives back each month
	RaisedPaise int64   `json:"raisedPaise,omitempty"` // how far the latest charge is above the usual one
	DaysUntil   int     `json:"daysUntil"`
}

func (st *State) Subscriptions() []Subscription {
	out := []Subscription{}
	for _, r := range st.Recurring {
		if r.Status != "active" {
			continue
		}
		s := Subscription{Name: r.Name, Cadence: r.Cadence, AmountPaise: r.AmountPaise, YearlyPaise: r.MonthlyPaise * 12, RunwayMonth: st.RunwayDays(r.MonthlyPaise), DaysUntil: r.DaysUntil}
		if raised(r) {
			s.RaisedPaise = r.LastPaise - r.AmountPaise
		}
		out = append(out, s)
	}
	return out
}

func raised(r insights.Recurring) bool {
	return r.Count >= 3 && float64(r.LastPaise) >= 1.1*float64(r.AmountPaise)
}

// Confidence says how much of the money the numbers can see. A wrong "you're safe" is worse than no advice.
type Confidence struct {
	AccountsSeen  int      `json:"accountsSeen"`
	AccountsTotal int      `json:"accountsTotal"`
	Notes         []string `json:"notes"`
}

func (st *State) Confidence() Confidence {
	c := Confidence{AccountsSeen: st.AccountsMoved, AccountsTotal: st.AccountsTotal, Notes: []string{}}
	if !st.salarySet() {
		c.Notes = append(c.Notes, "No salary date set, so future income is not counted.")
	}
	if len(st.Days) < 14 {
		c.Notes = append(c.Notes, "Under two weeks of history, so your usual pace is a rough guess.")
	}
	n := 0
	for i := range st.Cards {
		if st.Cards[i].BillingDay == 0 || st.Cards[i].DueDay == 0 {
			n++
		}
	}
	if n > 0 {
		c.Notes = append(c.Notes, plural(n, "card")+" without billing and due dates cannot be counted.")
	}
	return c
}
