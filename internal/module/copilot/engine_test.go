package copilot

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/insights"
	"ledger-backend/internal/module/mandates"
)

// 2 Nov 2026 is a Monday. Salary on the 12th is 10 days away.
var testNow = time.Date(2026, 11, 2, 15, 0, 0, 0, ist.Loc)

func rupee(r int64) int64 { return r * 100 }

func base() *State {
	st := &State{Now: testNow, Cash: rupee(50_000), Salary: rupee(100_000), SalaryDay: 12, Buffer: rupee(10_000)}
	for i := 30; i >= 1; i-- {
		st.Days = append(st.Days, DayRow{Date: st.date(-i).Format("2006-01-02"), Spent: rupee(1_000)})
	}
	return st
}

func TestRs(t *testing.T) {
	for p, want := range map[int64]string{0: "₹0", 99_900: "₹999", 123_400: "₹1,234", 12_345_600: "₹1,23,456", 1_234_567_800: "₹1,23,45,678", -123_400: "-₹1,234"} {
		if got := Rs(p); got != want {
			t.Errorf("Rs(%d) = %s, want %s", p, got, want)
		}
	}
}

func TestSafeSubtractsCommitmentsAndBuffer(t *testing.T) {
	st := base()
	st.Mandates = []mandates.Occ{{Name: "Rent", Paise: rupee(20_000), Days: 3}, {Name: "After salary", Paise: rupee(99_000), Days: 40}}
	st.Emis = []Dated{{"Car", 5, rupee(5_000)}}
	s := st.SafeNow()
	// (50,000 - 20,000 rent - 5,000 EMI - 10,000 buffer) / 10 days; what falls after salary is not counted.
	if s.TodayPaise != rupee(1_500) || s.CommittedPaise != rupee(25_000) || s.DaysToSalary != 10 {
		t.Fatalf("safe = %+v", s)
	}
}

func TestSafeCarriesOverAndKeepsTodaysAllowance(t *testing.T) {
	st := base()
	st.Cash -= rupee(1_000) // bank spending has already left cash
	st.TodaySpent = rupee(1_000)
	s := st.SafeNow()
	if s.TodayPaise != rupee(4_000) {
		t.Fatalf("today's allowance moved with today's spending: %d", s.TodayPaise)
	}
	if s.TomorrowPaise != rupee(39_000)/9 {
		t.Fatalf("tomorrow = %d", s.TomorrowPaise)
	}
	// A card purchase has not left cash, but it still comes off tomorrow.
	st = base()
	st.TodaySpent, st.TodayCard = rupee(1_000), rupee(1_000)
	if s := st.SafeNow(); s.TodayPaise != rupee(4_000) || s.TomorrowPaise != rupee(39_000)/9 {
		t.Fatalf("card spend: %+v", s)
	}
}

func TestSafeTones(t *testing.T) {
	for _, c := range []struct {
		spent int64
		tone  string
	}{{rupee(2_000), "good"}, {rupee(3_000), "warn"}, {rupee(4_000), "warn"}, {rupee(4_001), "bad"}} {
		st := base()
		st.TodaySpent, st.TodayCard = c.spent, c.spent
		if s := st.SafeNow(); s.Tone != c.tone {
			t.Errorf("spent %d: tone %s, want %s (safe %d)", c.spent, s.Tone, c.tone, s.TodayPaise)
		}
	}
}

func TestPaceIgnoresBigDays(t *testing.T) {
	st := base()
	st.Days[10].Spent = rupee(40_000) // rent day
	st.Days[11].Spent = rupee(30_000)
	if p := st.PaceDaily(); p != rupee(1_000) {
		t.Fatalf("pace = %d", p)
	}
}

func TestCheckVerdicts(t *testing.T) {
	st := base()
	if c := st.Check(CheckInput{AmountPaise: rupee(5_000)}); c.Verdict != "buy" || c.Earliest != "" {
		t.Errorf("small purchase: %s (%v)", c.Verdict, c.Reasons)
	}
	// Too much for now, easy after salary lands.
	c := st.Check(CheckInput{AmountPaise: rupee(45_000)})
	if c.Verdict != "wait" || c.Earliest == "" || c.Earliest < "2026-11-12" {
		t.Errorf("big purchase: %s earliest %s", c.Verdict, c.Earliest)
	}
	if c.RunwayCost <= 0 || c.RunwayAfter >= c.RunwayBefore || len(c.Scenarios) != 4 || c.Scenarios[0].Label != "Now" {
		t.Errorf("shape: %+v", c)
	}
	// Never affordable.
	if c := st.Check(CheckInput{AmountPaise: rupee(9_000_000)}); c.Verdict != "skip" {
		t.Errorf("huge purchase: %s earliest %s", c.Verdict, c.Earliest)
	}
}

func TestCheckNamesWhatItLeavesShort(t *testing.T) {
	st := base()
	st.Mandates = []mandates.Occ{{Name: "Rent", Paise: rupee(30_000), Days: 3}}
	c := st.Check(CheckInput{AmountPaise: rupee(30_000)})
	if c.Scenarios[0].Uncovered != "Rent" || c.Scenarios[0].Passes {
		t.Fatalf("now = %+v", c.Scenarios[0])
	}
	if !strings.Contains(strings.Join(c.Reasons, " "), "Rent") {
		t.Fatalf("reasons = %v", c.Reasons)
	}
}

func TestCheckSpecificDay(t *testing.T) {
	st := base()
	if c := st.Check(CheckInput{AmountPaise: rupee(45_000), Date: "2026-11-20"}); c.Verdict != "buy" {
		t.Fatalf("after salary: %s", c.Verdict)
	}
}

func TestWantSkippedSoonerThanNeed(t *testing.T) {
	// Just enough saved in about 70 days: a need waits, a want is told to rethink.
	st := base()
	st.Salary = rupee(35_000)
	st.Days = nil
	for i := 30; i >= 1; i-- {
		st.Days = append(st.Days, DayRow{Date: st.date(-i).Format("2006-01-02"), Spent: rupee(1_000)})
	}
	amt := rupee(55_000)
	need, want := st.Check(CheckInput{AmountPaise: amt, Need: true}), st.Check(CheckInput{AmountPaise: amt})
	if need.Verdict == "buy" || want.Verdict == "buy" {
		t.Fatalf("need %s want %s", need.Verdict, want.Verdict)
	}
}

func TestOnCardDelaysThePayment(t *testing.T) {
	st := base()
	tb, td, avail := 5, 20, rupee(100_000)
	st.Cards = []cards.View{{Card: cards.Card{Name: "A", BillingDay: 7, DueDay: 22}, DaysToBilling: &tb, DaysToDue: &td, AvailablePaise: &avail}}
	cash := st.Check(CheckInput{AmountPaise: rupee(38_000)})
	card := st.Check(CheckInput{AmountPaise: rupee(38_000), OnCard: true})
	if cash.Verdict == "buy" || card.Verdict != "buy" {
		t.Fatalf("cash %s card %s", cash.Verdict, card.Verdict)
	}
}

func TestWeekAhead(t *testing.T) {
	st := base()
	st.Mandates = []mandates.Occ{{Name: "Rent", Paise: rupee(8_000), Days: 3}, {Name: "Far", Paise: rupee(1), Days: 30}}
	w := st.WeekAhead(7)
	if w.OutPaise != rupee(8_000) || w.InPaise != 0 || len(w.Events) != 1 {
		t.Fatalf("week = %+v", w)
	}
	if w.LowestPaise != rupee(50_000-8_000-7_000) || !w.AboveBuffer {
		t.Fatalf("lowest = %d", w.LowestPaise)
	}
	if !strings.Contains(w.Text(st), "Rent 5 Nov") {
		t.Fatalf("text = %s", w.Text(st))
	}
}

func TestHabits(t *testing.T) {
	st := base()
	for i := range st.Days {
		if d, _ := time.ParseInLocation("2006-01-02", st.Days[i].Date, ist.Loc); d.Weekday() == time.Friday {
			st.Days[i].Spent = rupee(5_000)
		}
	}
	st.Days[len(st.Days)-1].Spent, st.Days[len(st.Days)-2].Spent = 0, 0
	h := st.Habits()
	if h.PriciestDay != "Friday" || h.NoSpendStreak != 2 || h.UnderStreak == 0 {
		t.Fatalf("habits = %+v", h)
	}
}

func TestCardAdvice(t *testing.T) {
	st := base()
	b1, d1, b2, d2, a := 25, 15, 2, 15, rupee(50_000)
	util := 0.62
	st.Cards = []cards.View{
		{Card: cards.Card{Name: "Long", BillingDay: 27, DueDay: 17, LimitPaise: rupee(100_000)}, DaysToBilling: &b1, DaysToDue: &d1, AvailablePaise: &a, Utilisation: &util, UsedPaise: rupee(62_000)},
		{Card: cards.Card{Name: "Short", BillingDay: 4, DueDay: 17}, DaysToBilling: &b2, DaysToDue: &d2, AvailablePaise: &a, Statement: &cards.Statement{UnpaidPaise: rupee(9_000), MinDuePaise: rupee(500), InterestIfMinPaise: rupee(300)}},
	}
	all := st.CardAdvice()
	kinds := map[string]Advice{}
	for _, a := range all {
		kinds[a.Kind] = a
	}
	if kinds["timing"].Card != "Long" || !strings.Contains(kinds["timing"].Body, "Short") {
		t.Errorf("timing = %+v", kinds["timing"])
	}
	if kinds["utilisation"].StakePaise != rupee(32_000) {
		t.Errorf("utilisation = %+v", kinds["utilisation"])
	}
	if !strings.Contains(kinds["payoff"].Body, "₹300") {
		t.Errorf("payoff = %+v", kinds["payoff"])
	}
}

func TestEmiCostIsPositive(t *testing.T) {
	e := emiCost(rupee(30_000))
	// 16% over 6 months is about ₹1,420, plus a ₹300 fee.
	if e.ExtraPaise < rupee(1_600) || e.ExtraPaise > rupee(1_800) {
		t.Fatalf("extra = %d", e.ExtraPaise)
	}
}

func TestRulesFireAndRankByMoney(t *testing.T) {
	st := base()
	st.TodaySpent, st.TodayCard = rupee(4_500), rupee(4_500)
	st.Mandates = []mandates.Occ{{Name: "Rent", Date: "2026-11-05", Paise: rupee(60_000), Days: 3}}
	all := st.Rules()
	has := func(kind string) bool {
		for _, n := range all {
			if n.Kind == kind {
				return true
			}
		}
		return false
	}
	if !has("commitment") || !has("pace_cash") || !has(kindWeek) {
		t.Fatalf("rules = %+v", all)
	}
	for i := 1; i < len(all); i++ {
		if all[i].StakePaise > all[i-1].StakePaise {
			t.Fatalf("not ranked: %+v", all)
		}
	}
}

func TestRuleIDsAreStablePerDay(t *testing.T) {
	st := base()
	st.TodaySpent, st.TodayCard = rupee(100_000), rupee(100_000)
	a, b := st.Rules(), st.Rules()
	if a[0].ID != b[0].ID {
		t.Fatal("same day should give the same id, or it would be sent twice")
	}
}

func TestPriceAndRenewalRules(t *testing.T) {
	st := base()
	st.Recurring = []insights.Recurring{
		{Name: "Netflix", Cadence: "monthly", Status: "active", Count: 6, AmountPaise: rupee(500), LastPaise: rupee(650), LastAt: testNow},
		{Name: "Insurance", Cadence: "yearly", Status: "active", Count: 2, AmountPaise: rupee(9_000), LastPaise: rupee(9_000), DaysUntil: 5, NextAt: testNow},
		{Name: "Gone", Cadence: "monthly", Status: "lapsed", Count: 6, AmountPaise: rupee(500), LastPaise: rupee(900)},
	}
	found := map[string]string{}
	for _, n := range st.Rules() {
		found[n.Kind] = n.Title
	}
	if !strings.Contains(found["price"], "Netflix went up 30%") || !strings.Contains(found["renewal"], "Insurance") {
		t.Fatalf("found = %v", found)
	}
	subs := st.Subscriptions()
	if len(subs) != 2 || subs[0].RaisedPaise != rupee(150) || subs[0].YearlyPaise != 0 {
		t.Fatalf("subs = %+v", subs)
	}
}

func TestWantRules(t *testing.T) {
	st := base()
	past, old := testNow.Add(-time.Hour), testNow.AddDate(0, 0, -8)
	id := bson.NewObjectID()
	st.Wants = []Want{
		{ID: bson.NewObjectID(), Name: "Shoes", AmountPaise: rupee(2_000), Status: "parked", RemindAt: &past},
		{ID: id, Name: "Phone", AmountPaise: rupee(20_000), Status: "bought", BoughtAt: &old},
	}
	found := map[string]Nudge{}
	for _, n := range st.Rules() {
		found[n.Kind] = n
	}
	if !strings.Contains(found["want_ready"].Title, "Shoes") || found["reflect"].Ref != id.Hex() {
		t.Fatalf("found = %+v", found)
	}
}

func TestPickKeepsSilence(t *testing.T) {
	all := []Nudge{{ID: "a", Kind: "commitment"}, {ID: "b", Kind: "card_due"}, {ID: "c", Kind: "price"}, {ID: "d", Kind: "pace_today"}, {ID: "w", Kind: kindWeek}}
	got := pick(all, map[string]bool{"a": true}, map[string]bool{"card_due": true}, 1)
	if len(got) != 2 || got[0].ID != "c" || got[1].ID != "w" {
		t.Fatalf("got = %+v", got)
	}
	if got := pick(all, nil, nil, -1); len(got) != 1 || got[0].ID != "w" {
		t.Fatalf("over the cap should leave only the brief: %+v", got)
	}
}

func TestIgnoredKinds(t *testing.T) {
	for _, c := range []struct {
		n, gone, acted int
		want           bool
	}{{5, 4, 0, true}, {5, 3, 0, false}, {4, 4, 0, false}, {6, 5, 1, false}} {
		if got := ignored(c.n, c.gone, c.acted); got != c.want {
			t.Errorf("%+v = %v", c, got)
		}
	}
}

func TestWantInputRejectsScriptLinks(t *testing.T) {
	in := WantInput{Name: "x", AmountPaise: 100, Link: "javascript:alert(1)"}
	if _, msg := in.Parse(); msg == "" {
		t.Fatal("a javascript: link must be rejected: it is rendered as an href")
	}
	in = WantInput{Name: "x", AmountPaise: 100, Link: "https://example.com/p", RemindOn: "2026-11-18"}
	if w, msg := in.Parse(); msg != "" || w.RemindAt == nil {
		t.Fatalf("msg = %s", msg)
	}
}

func TestRupeeViewConvertsPaise(t *testing.T) {
	v, err := rupeeView(Safe{TodayPaise: 123_400, Committed: []Event{{Name: "Rent", Paise: -500}}})
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["todayRupees"] != 1234.0 || m["todayPaise"] != nil {
		t.Fatalf("m = %v", m)
	}
	if m["committed"].([]any)[0].(map[string]any)["rupees"] != -5.0 {
		t.Fatalf("nested = %v", m["committed"])
	}
}
