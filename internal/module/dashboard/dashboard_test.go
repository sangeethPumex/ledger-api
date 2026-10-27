package dashboard

import (
	"testing"
	"time"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/emis"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/profile"
)

func fl(name, tone string, p int64) Flow {
	return Flow{ID: name, Name: name, Tone: tone, Paise: p, Txns: []TxnView{}}
}

func TestBuildCashflowBalances(t *testing.T) {
	income := []Flow{fl("Salary", "income", 100000)}
	out := []Flow{fl("Rent", "spend", 30000), fl("Food", "discretionary", 20000), fl("SIP", "invest", 10000)}
	cf := buildCashflow(income, out, 0, 0)
	if cf.Income != 100000 || cf.Spent != 50000 || cf.Invested != 10000 || cf.Net != 40000 {
		t.Fatalf("totals %+v", cf)
	}
	var l, r int64
	for _, f := range cf.Sources {
		l += f.Paise
	}
	for _, f := range cf.Dests {
		r += f.Paise
	}
	if l != r {
		t.Errorf("sankey sides differ: %d vs %d", l, r)
	}
	if cf.SavingsRate == nil || *cf.SavingsRate != 0.5 {
		t.Errorf("savings rate %v", cf.SavingsRate)
	}

	// spending beyond income appears as a source so both sides still match
	cf = buildCashflow([]Flow{fl("Salary", "income", 1000)}, []Flow{fl("Rent", "spend", 3000)}, 0, 0)
	if cf.Net != -2000 || cf.Sources[len(cf.Sources)-1].Name != "From savings" {
		t.Errorf("shortfall: %+v", cf.Sources)
	}

	// many categories fold into one "Other"
	var many []Flow
	for i := 0; i < 12; i++ {
		many = append(many, fl(string(rune('a'+i)), "spend", int64(100-i)))
	}
	top := topFlows(many, 7, "Other", "spend")
	if len(top) != 8 || top[7].Name != "Other" || top[7].Count != 0 {
		t.Errorf("top: %d flows, last %q", len(top), top[len(top)-1].Name)
	}
}

func TestParsePeriod(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, ist.Loc)
	r, err := ParsePeriod("month", "", "", now)
	if err != nil || r.From.Day() != 1 || r.To.Month() != 11 {
		t.Errorf("month %v %v", r, err)
	}
	if r, _ := ParsePeriod("3m", "", "", now); r.From.Month() != 8 {
		t.Errorf("3m from %v", r.From)
	}
	if r, _ := ParsePeriod("custom", "2026-10-01", "2026-10-03", now); r.To.Day() != 4 {
		t.Errorf("custom end should be exclusive next day, got %v", r.To)
	}
	for _, c := range [][3]string{{"custom", "", ""}, {"custom", "2026-10-05", "2026-10-01"}, {"custom", "2025-01-01", "2026-12-31"}, {"weekly", "", ""}} {
		if _, err := ParsePeriod(c[0], c[1], c[2], now); err == nil {
			t.Errorf("%v accepted", c)
		}
	}
}

func TestUpcoming(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, ist.Loc)
	em := &emis.Summary{Monthly: 10000, Outstanding: 200000, ActiveCount: 1, Active: []emis.Brief{{Name: "Car", Amount: 10000, DueDay: 30}}}
	in := upcomingIn{Cash: 3000000, Profile: &profile.Profile{Salary: 100000, SalaryDay: 1, BufferTarget: 10000}, Emis: em, Funds: &funds.Summary{}, Last30: 3000000, Now: now}

	u := upcoming(in)
	if u.DaysToSalary != 4 || u.CommittedDue != 1000000 || u.DailyTarget != 250000 {
		t.Errorf("salary in 4 days, EMI due before it: %+v", u)
	}
	if u.RunwayDays != nil || u.RunwayTone != "good" {
		t.Errorf("income covers outgoings, expected no runway: %+v", u.RunwayDays)
	}

	in.Profile = &profile.Profile{SalaryDay: 1}
	u = upcoming(in)
	if u.RunwayDays == nil || *u.RunwayDays != 22 || u.RunwayTone != "bad" {
		t.Errorf("no income: runway %v tone %s", u.RunwayDays, u.RunwayTone)
	}

	in.Today = 999999999
	if u = upcoming(in); u.DailyTone != "bad" && u.DailyTarget > 0 {
		t.Errorf("overspent tone %s", u.DailyTone)
	}
}
