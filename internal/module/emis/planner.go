package emis

import (
	"context"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const maxPlanMonths = 600

// Debt is one active obligation as the planner sees it. Rupees. Rate is yearly percent; purchases without one cost nothing extra.
type Debt struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Balance float64 `json:"balance"`
	Rate    float64 `json:"rate"`
	Min     float64 `json:"min"` // the instalment
}

type Cleared struct {
	Name  string `json:"name"`
	Month int    `json:"month"` // months from now
}

// Strategy is the outcome of one way of paying. Saved figures are against the "scheduled" plan and are zero on it.
type Strategy struct {
	Method        string    `json:"method"` // scheduled | avalanche | snowball
	Months        int       `json:"months"`
	Interest      float64   `json:"interest"`
	NeverEnds     bool      `json:"neverEnds"` // still owing after maxPlanMonths
	Order         []Cleared `json:"order"`
	InterestSaved float64   `json:"interestSaved"`
	MonthsSaved   int       `json:"monthsSaved"`
}

// Simulate pays the debts month by month. Interest is balance x rate / 12 added first, then every instalment is paid.
// "scheduled" pays instalments only: a cleared loan's instalment goes back to the user. The other methods keep the whole budget
// (all instalments + extra) going, and the money left after instalments goes to one debt at a time:
// avalanche to the highest rate first (least interest), snowball to the smallest balance first (quickest wins).
func Simulate(debts []Debt, extra float64, method string) Strategy {
	bal := make([]float64, len(debts))
	var budget float64
	for i, d := range debts {
		bal[i], budget = d.Balance, budget+d.Min
	}
	budget += extra
	s := Strategy{Method: method, Order: []Cleared{}}
	order := make([]int, len(debts)) // indexes in the order extra money is spent
	for i := range order {
		order[i] = i
	}
	done := make([]bool, len(debts)) // already in s.Order
	open := func(i int) bool { return bal[i] > 0.005 }
	for m := 1; m <= maxPlanMonths; m++ {
		left := 0
		for i, d := range debts {
			if open(i) {
				left++
				in := bal[i] * d.Rate / 1200
				bal[i], s.Interest = bal[i]+in, s.Interest+in
			}
		}
		if left == 0 {
			break
		}
		pool := budget
		for i, d := range debts {
			if open(i) {
				p := min(d.Min, bal[i])
				bal[i], pool = bal[i]-p, pool-p
			}
		}
		if method != "scheduled" {
			sort.SliceStable(order, func(a, b int) bool {
				x, y := order[a], order[b]
				if method == "snowball" {
					return bal[x] < bal[y] || (bal[x] == bal[y] && debts[x].Rate > debts[y].Rate)
				}
				return debts[x].Rate > debts[y].Rate || (debts[x].Rate == debts[y].Rate && bal[x] < bal[y])
			})
			for _, i := range order {
				if open(i) && pool > 0 {
					p := min(pool, bal[i])
					bal[i], pool = bal[i]-p, pool-p
				}
			}
		}
		for i, d := range debts {
			if !open(i) && !done[i] {
				bal[i], done[i] = 0, true
				s.Order, s.Months = append(s.Order, Cleared{d.Name, m}), m
			}
		}
	}
	for i := range debts {
		s.NeverEnds = s.NeverEnds || open(i)
	}
	s.Interest = r2(s.Interest)
	return s
}

// PlanResult compares paying as scheduled with the two prepayment strategies, using the same extra monthly amount.
type PlanResult struct {
	Extra      float64    `json:"extra"`
	Debts      []Debt     `json:"debts"`
	Strategies []Strategy `json:"strategies"` // scheduled, avalanche, snowball
}

func Plan(debts []Debt, extra float64) PlanResult {
	base := Simulate(debts, 0, "scheduled")
	out := PlanResult{Extra: extra, Debts: debts, Strategies: []Strategy{base}}
	for _, m := range []string{"avalanche", "snowball"} {
		s := Simulate(debts, extra, m)
		if !base.NeverEnds && !s.NeverEnds {
			s.InterestSaved, s.MonthsSaved = r2(base.Interest-s.Interest), base.Months-s.Months
		}
		out.Strategies = append(out.Strategies, s)
	}
	return out
}

// Plan loads the active obligations (without their payment history and statements, which can be long) and compares strategies.
func (s *Service) Plan(ctx context.Context, extra float64) (PlanResult, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetProjection(bson.M{"payments": 0, "ledger": 0, "rates": 0}).SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return PlanResult{}, err
	}
	var all []Emi
	if err := cur.All(ctx, &all); err != nil {
		return PlanResult{}, err
	}
	debts := []Debt{}
	for i := range all {
		e := &all[i]
		if !Active(e) {
			continue
		}
		d := Debt{ID: e.ID.Hex(), Name: e.Name, Balance: r2(Left(e)), Min: e.Amount}
		if e.Rate != nil {
			d.Rate = *e.Rate
		}
		debts = append(debts, d)
	}
	return Plan(debts, extra), nil
}
