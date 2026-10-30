// Package dashboard computes everything the dashboard shows: position, cash flow (Sankey data), upcoming obligations and AI advice.
// Transaction totals come from Mongo aggregations; the arithmetic that combines them lives here. All money in responses is paise.
package dashboard

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/emis"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/profile"
)

// investPattern marks categories that are money moved into investments, not consumption. Also used inside the aggregation.
const investPattern = "invest|sip|mutual|stock|saving|fd|deposit|nps|ppf"

var (
	investRe        = regexp.MustCompile("(?i)" + investPattern)
	discretionaryRe = regexp.MustCompile("(?i)food|dining|restaurant|eat|shopping|entertain|travel|subscription|swiggy|zomato|movie|leisure")
)

func paise(rupees float64) int64 { return int64(math.Round(rupees * 100)) }

type TxnView struct {
	ID     string `json:"id"`
	Vendor string `json:"vendor"`
	Source string `json:"source"`
	At     string `json:"at"`
	Paise  int64  `json:"paise"`
	Credit bool   `json:"credit"`
}

// Flow is one node of the money-flow chart. Tone: income, spend, discretionary, invest, gap or net.
type Flow struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Tone  string    `json:"tone"`
	Paise int64     `json:"paise"`
	Count int       `json:"count"`
	Txns  []TxnView `json:"txns"`
}

type Day struct {
	Date  string `json:"date"`
	Paise int64  `json:"paise"`
	Top   *struct {
		Vendor   string `json:"vendor"`
		Category string `json:"category"`
		Paise    int64  `json:"paise"`
	} `json:"top,omitempty"`
}

type Cashflow struct {
	Income       int64    `json:"incomePaise"`
	Spent        int64    `json:"spentPaise"`
	Invested     int64    `json:"investedPaise"`
	Net          int64    `json:"netPaise"`
	SavingsRate  *float64 `json:"savingsRate,omitempty"` // (income - spent) / income; investments count as saved
	Sources      []Flow   `json:"sources"`
	Dests        []Flow   `json:"dests"`
	Daily        []Day    `json:"daily"`
	EarlierSpent int64    `json:"earlierSpentPaise"` // before Catalyst, already included above
	// CardPayments is what was paid toward credit card bills in the period. The purchases it settles are already in Spent, so it is shown
	// beside the flow and never added to any total.
	CardPayments *Flow `json:"cardPayments,omitempty"`
}

func classify(name string) string {
	switch {
	case investRe.MatchString(name):
		return "invest"
	case discretionaryRe.MatchString(name):
		return "discretionary"
	}
	return "spend"
}

func sortFlows(f []Flow) {
	sort.SliceStable(f, func(i, j int) bool { return f[i].Paise > f[j].Paise })
}

func topTxns(t []TxnView, n int) []TxnView {
	sort.SliceStable(t, func(i, j int) bool { return t[i].Paise > t[j].Paise })
	if len(t) > n {
		t = t[:n]
	}
	return t
}

// topFlows keeps the `keep` largest flows and folds the rest into one "other" flow.
func topFlows(flows []Flow, keep int, other, tone string) []Flow {
	sortFlows(flows)
	if len(flows) <= keep+1 {
		return flows
	}
	o := Flow{ID: "other-" + other, Name: other, Tone: tone, Txns: []TxnView{}}
	for _, f := range flows[keep:] {
		o.Paise += f.Paise
		o.Count += f.Count
		o.Txns = append(o.Txns, f.Txns...)
	}
	o.Txns = topTxns(o.Txns, 8)
	return append(flows[:keep:keep], o)
}

// buildCashflow turns classified flows into the chart model. Income = credits typed "income"; spending = expense/atm/emi debits;
// categories named like investments count as invested, not spent. Both sides always add up: a shortfall shows as "From savings".
func buildCashflow(income, out []Flow, earlierIncome, earlierSpent int64) Cashflow {
	if earlierIncome > 0 {
		income = append(income, Flow{ID: "in-earlier", Name: "Earlier this month", Tone: "income", Paise: earlierIncome, Txns: []TxnView{}})
	}
	if earlierSpent > 0 {
		out = append(out, Flow{ID: "out-earlier", Name: "Before Catalyst", Tone: "spend", Paise: earlierSpent, Txns: []TxnView{}})
	}
	var cf Cashflow
	var spend, invest []Flow
	for _, f := range income {
		cf.Income += f.Paise
	}
	for _, f := range out {
		if f.Tone == "invest" {
			cf.Invested += f.Paise
			invest = append(invest, f)
		} else {
			cf.Spent += f.Paise
			spend = append(spend, f)
		}
	}
	cf.Net, cf.EarlierSpent = cf.Income-cf.Spent-cf.Invested, earlierSpent
	if cf.Income > 0 {
		r := float64(cf.Income-cf.Spent) / float64(cf.Income)
		cf.SavingsRate = &r
	}
	cf.Sources = topFlows(income, 3, "Other income", "income")
	if cf.Net < 0 {
		cf.Sources = append(cf.Sources, Flow{ID: "gap", Name: "From savings", Tone: "gap", Paise: -cf.Net, Txns: []TxnView{}})
	}
	sortFlows(invest)
	cf.Dests = append(topFlows(spend, 7, "Other", "spend"), invest...)
	if cf.Net > 0 {
		cf.Dests = append(cf.Dests, Flow{ID: "net", Name: "Left over", Tone: "net", Paise: cf.Net, Txns: []TxnView{}})
	}
	if cf.Sources == nil {
		cf.Sources = []Flow{}
	}
	if cf.Dests == nil {
		cf.Dests = []Flow{}
	}
	return cf
}

type Period struct {
	Key  string `json:"key"`
	From string `json:"from"` // RFC3339
	To   string `json:"to"`   // exclusive
}

type Range struct{ From, To time.Time }

// ParsePeriod resolves a period in IST. `to` is exclusive. Custom takes yyyy-mm-dd, the last day inclusive, at most a year.
func ParsePeriod(key, from, to string, now time.Time) (Range, error) {
	n := now.In(ist.Loc)
	y, m := n.Year(), int(n.Month())
	at := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, ist.Loc) }
	switch key {
	case "", "month":
		return Range{at(y, m, 1), at(y, m+1, 1)}, nil
	case "last":
		return Range{at(y, m-1, 1), at(y, m, 1)}, nil
	case "3m":
		return Range{at(y, m-2, 1), at(y, m+1, 1)}, nil
	case "custom":
		f, err1 := time.ParseInLocation("2006-01-02", from, ist.Loc)
		t, err2 := time.ParseInLocation("2006-01-02", to, ist.Loc)
		switch {
		case err1 != nil || err2 != nil:
			return Range{}, fmt.Errorf("custom period needs from and to as YYYY-MM-DD")
		case t.Before(f):
			return Range{}, fmt.Errorf("from must not be after to")
		case t.Sub(f) > 366*24*time.Hour:
			return Range{}, fmt.Errorf("custom period is limited to a year")
		}
		return Range{f, t.AddDate(0, 0, 1)}, nil
	}
	return Range{}, fmt.Errorf("period must be month, last, 3m or custom")
}

type Tone string

type Upcoming struct {
	SalaryPaise    int64    `json:"salaryPaise"`
	SalaryDay      int      `json:"salaryDay"`
	DaysToSalary   int      `json:"daysToSalary"`
	EmiMonthly     int64    `json:"emiMonthlyPaise"`
	EmiOutstanding int64    `json:"emiOutstandingPaise"`
	EmiActive      int      `json:"emiActive"`
	EmiShare       float64  `json:"emiShare"` // of salary
	EmiTone        Tone     `json:"emiTone"`
	NextEmi        *Next    `json:"nextEmi,omitempty"`
	SipMonthly     int64    `json:"sipMonthlyPaise"`
	NextSip        *Next    `json:"nextSip,omitempty"`
	MonthlyNet     int64    `json:"monthlyNetPaise"`
	MonthlyBurn    int64    `json:"monthlyBurnPaise"` // net outflow per month when negative, else 0
	RunwayDays     *int     `json:"runwayDays,omitempty"`
	RunwayTone     Tone     `json:"runwayTone"`
	// If income stopped: spending, fixed costs and EMIs continue, SIPs pause. Cash-only vs cash plus every investment sold.
	StopBurn        int64 `json:"stopBurnPaise"`
	RunwayCashDays  *int  `json:"runwayCashDays,omitempty"`
	RunwayWorthDays *int  `json:"runwayWorthDays,omitempty"`
	DailyTarget   int64    `json:"dailyTargetPaise"`
	TodaySpent     int64    `json:"todaySpentPaise"`
	DailyTone      Tone     `json:"dailyTone"`
	CommittedDue   int64    `json:"committedDuePaise"` // EMIs and SIPs debiting before the next salary
	BufferTarget   int64    `json:"bufferTargetPaise"`
	BufferRatio    *float64 `json:"bufferRatio,omitempty"`
	BufferTone     Tone     `json:"bufferTone"`
	AvgDailySpend  int64    `json:"avgDailySpendPaise"`
}

type Next struct {
	Name  string `json:"name"`
	Paise int64  `json:"paise"`
	Days  int    `json:"days"`
}

type upcomingIn struct {
	Cash       int64
	NetWorth   int64 // cash + investments
	Profile   *profile.Profile
	Emis       *emis.Summary
	Funds      *funds.Summary
	Last30     int64 // spend over the last 30 days
	MonthSpent int64 // tracked spend since the 1st
	Today      int64
	Now        time.Time
}

// upcoming is the forward-looking arithmetic: runway, today's budget, buffer and commitments.
func upcoming(in upcomingIn) Upcoming {
	p, now := in.Profile, in.Now.In(ist.Loc)
	u := Upcoming{SalaryPaise: paise(p.Salary), SalaryDay: p.SalaryDay, DaysToSalary: 30, TodaySpent: in.Today, BufferTarget: paise(p.BufferTarget), RunwayTone: "good", EmiTone: "neutral", DailyTone: "neutral", BufferTone: "neutral"}
	if p.Salary > 0 {
		u.DaysToSalary = ist.DaysUntil(p.SalaryDay, now)
	}

	var due float64 // rupees
	u.EmiMonthly, u.EmiOutstanding, u.EmiActive = paise(in.Emis.Monthly), paise(in.Emis.Outstanding), in.Emis.ActiveCount
	for _, e := range in.Emis.Active {
		if ist.DaysUntil(e.DueDay, now) <= u.DaysToSalary {
			due += e.Amount
		}
	}
	if in.Emis.Next != nil {
		u.NextEmi = &Next{in.Emis.Next.Name, paise(in.Emis.Next.Amount), in.Emis.Next.Days}
	}
	sip := p.SIP
	if in.Funds.Count > 0 {
		sip = in.Funds.SipMonthly
	}
	u.SipMonthly = paise(sip)
	for _, s := range in.Funds.Sips {
		if ist.DaysUntil(s.Day, now) <= u.DaysToSalary {
			due += s.Amount
		}
	}
	if in.Funds.NextSip != nil {
		u.NextSip = &Next{in.Funds.NextSip.Name, paise(in.Funds.NextSip.Amount), in.Funds.NextSip.Days}
	}
	u.CommittedDue = paise(due)

	// Spend rate: 30-day average, unless pre-Catalyst spend is known, then this month's total over the days elapsed.
	u.AvgDailySpend = in.Last30 / 30
	if p.Prior.YM == now.Format("2006-01") && p.Prior.Spent > 0 {
		u.AvgDailySpend = (in.MonthSpent + paise(p.Prior.Spent)) / int64(now.Day())
	}

	u.DailyTarget = max(in.Cash-u.BufferTarget-u.CommittedDue, 0) / int64(u.DaysToSalary)
	switch {
	case u.DailyTarget == 0:
	case in.Today > u.DailyTarget:
		u.DailyTone = "bad"
	case float64(in.Today) > float64(u.DailyTarget)*0.8:
		u.DailyTone = "warn"
	default:
		u.DailyTone = "good"
	}

	u.MonthlyNet = u.SalaryPaise - u.AvgDailySpend*30 - paise(p.FixedMonthly) - u.SipMonthly - u.EmiMonthly
	if u.MonthlyNet < 0 {
		u.MonthlyBurn = -u.MonthlyNet
		d := max(int(float64(in.Cash)/(float64(u.MonthlyBurn)/30)), 0)
		u.RunwayDays = &d
		switch {
		case d < 30:
			u.RunwayTone = "bad"
		case d < 90:
			u.RunwayTone = "warn"
		}
	}

	if u.StopBurn = u.AvgDailySpend*30 + paise(p.FixedMonthly) + u.EmiMonthly; u.StopBurn > 0 {
		days := func(pool int64) *int { d := max(int(float64(pool)/(float64(u.StopBurn)/30)), 0); return &d }
		u.RunwayCashDays, u.RunwayWorthDays = days(in.Cash), days(max(in.NetWorth, in.Cash))
	}

	if u.BufferTarget > 0 {
		r := float64(in.Cash) / float64(u.BufferTarget)
		u.BufferRatio = &r
		switch {
		case r < 1:
			u.BufferTone = "bad"
		case r < 1.25:
			u.BufferTone = "warn"
		default:
			u.BufferTone = "good"
		}
	}

	if p.Salary > 0 && u.EmiMonthly > 0 {
		u.EmiShare = float64(u.EmiMonthly) / float64(u.SalaryPaise)
	}
	switch {
	case u.EmiMonthly == 0:
	case u.EmiShare > 0.5:
		u.EmiTone = "bad"
	case u.EmiShare > 0.35:
		u.EmiTone = "warn"
	default:
		u.EmiTone = "good"
	}
	return u
}
