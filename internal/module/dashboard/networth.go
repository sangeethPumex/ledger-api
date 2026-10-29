package dashboard

import (
	"context"
	"errors"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/snapshots"
)

// monthDoc is one row of the `networth` collection, keyed by IST month ("2026-09"). The current month's row is rewritten on every
// dashboard load, so once the month rolls over it holds the last value seen: the month-end. Net worth is cash + investments;
// money owed is kept beside it, never subtracted. Cash and investments are absent on rows entered by hand; their owed comes from the loan statement.
type monthDoc struct {
	ID          string `bson:"_id"`
	Date        string `bson:"date"`
	NetWorth    int64  `bson:"net_worth"`
	Owed        *int64 `bson:"owed,omitempty"`
	Cash        *int64 `bson:"cash,omitempty"`
	Investments *int64 `bson:"investments,omitempty"`
}

// seedMonths is net worth entered by hand for months before Catalyst recorded them (paise). Inserted once, never overwritten.
var seedMonths = []monthDoc{
	{ID: "2026-05", Date: "2026-05-31", NetWorth: 29009000},
	{ID: "2026-06", Date: "2026-06-30", NetWorth: 29790200},
	{ID: "2026-07", Date: "2026-07-31", NetWorth: 30300200},
	{ID: "2026-08", Date: "2026-08-31", NetWorth: 31083000},
	{ID: "2026-09", Date: "2026-09-30", NetWorth: 32389200},
}

func (s *Service) seed(ctx context.Context) error {
	for _, m := range seedMonths {
		if _, err := s.months.UpdateOne(ctx, bson.M{"_id": m.ID}, bson.M{"$setOnInsert": m}, options.UpdateOne().SetUpsert(true)); err != nil {
			return err
		}
		// Owed on that date comes from the loan statement; only rows with no owed figure are touched.
		owed, ok, err := s.emis.OwedOn(ctx, m.Date)
		if err != nil {
			return err
		}
		if ok {
			if _, err := s.months.UpdateOne(ctx, bson.M{"_id": m.ID, "owed": bson.M{"$exists": false}}, bson.M{"$set": bson.M{"owed": paise(owed)}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// NWPoint is one plotted point. Optional fields are absent when the figure was never recorded. Change is against the previous point
// in the same series (history, then each scenario).
type NWPoint struct {
	Date        string `json:"date"`
	NetWorth    int64  `json:"netWorthPaise"`
	Owed        *int64 `json:"owedPaise,omitempty"`
	Cash        *int64 `json:"cashPaise,omitempty"`
	Investments *int64 `json:"investmentsPaise,omitempty"`
	Change      int64  `json:"changePaise"`
}

type Scenario struct {
	Name        string    `json:"name"`
	Assumptions string    `json:"assumptions"`
	Points      []NWPoint `json:"points"`
}

type NetWorthResponse struct {
	History   []NWPoint  `json:"history"`
	Scenarios []Scenario `json:"scenarios"`
	Method    string     `json:"method"`
}

const projectionMethod = "Rule-based projection from your salary, recent average spending, SIPs and EMIs with fixed return assumptions. " +
	"It is not a statistical forecast and has not been validated against past data."

func (s *Service) record(ctx context.Context, now time.Time, p Position) error {
	t := now.In(ist.Loc)
	_, err := s.months.UpdateOne(ctx, bson.M{"_id": t.Format("2006-01")},
		bson.M{"$set": bson.M{"date": t.Format("2006-01-02"), "net_worth": p.NetWorth, "owed": p.Liabilities, "cash": p.Cash, "investments": p.Current}},
		options.UpdateOne().SetUpsert(true))
	return err
}

// lastMonthChange is net worth now against the last recorded month-end, if there is one.
func (s *Service) lastMonthChange(ctx context.Context, now time.Time, current int64) (change int64, since string, ok bool, err error) {
	t := now.In(ist.Loc)
	var m monthDoc
	err = s.months.FindOne(ctx, bson.M{"_id": time.Date(t.Year(), t.Month()-1, 1, 0, 0, 0, 0, ist.Loc).Format("2006-01")}).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, "", false, nil
	}
	return current - m.NetWorth, m.Date, err == nil, err
}

// UseSnapshots makes net-worth history read the daily snapshot rows.
func (s *Service) UseSnapshots(sn *snapshots.Service) { s.snaps = sn }

// dailyPoints is one point per day from the snapshot table; nil when there is none (or no table yet), so the month rows alone are used.
func (s *Service) dailyPoints(ctx context.Context, from, to string) []NWPoint {
	if s.snaps == nil {
		return nil
	}
	rows, err := s.snaps.Range(ctx, from, to)
	if err != nil {
		log.Printf("dashboard: snapshots: %v", err)
		return nil
	}
	out := make([]NWPoint, 0, len(rows))
	for _, r := range rows {
		out = append(out, NWPoint{Date: r.Date, NetWorth: r.NetWorth, Owed: r.Owed, Cash: ptr(r.Cash), Investments: ptr(r.Investments)})
	}
	return out
}

func ptr(v int64) *int64 { return &v }

func newPoint(date string, cash, inv, owed int64) NWPoint {
	return NWPoint{Date: date, NetWorth: cash + inv, Owed: ptr(owed), Cash: ptr(cash), Investments: ptr(inv)}
}

func setChanges(pts []NWPoint, prev int64) {
	for i := range pts {
		pts[i].Change, prev = pts[i].NetWorth-prev, pts[i].NetWorth
	}
}

var scenarios = []struct {
	name        string
	annualRet   float64
	spendFactor float64
	note        string
}{
	{"Expected", 0.08, 1, "8% a year on investments; spending at your recent average"},
	{"Conservative", 0, 1.15, "No investment growth; spending 15% above your recent average"},
	{"Optimistic", 0.12, 0.9, "12% a year on investments; spending 10% below your recent average"},
}

// project rolls `from` forward `months` months under each scenario. u.MonthlyNet already has salary, spending, fixed costs, SIPs and EMIs
// taken out of cash, so scenarios only adjust discretionary spend, add the SIP to investments and grow them. EMI payments reduce
// cash (and so net worth) and the amount owed.
// ponytail: an EMI cuts what is owed by its full amount (interest ignored) and runs forever; refine with the amortisation schedule.
func project(from NWPoint, u Upcoming, now time.Time, months int) []Scenario {
	spend := u.AvgDailySpend * 30
	out := make([]Scenario, 0, len(scenarios))
	for _, sc := range scenarios {
		cash, inv, owed := *from.Cash, *from.Investments, *from.Owed
		pts := make([]NWPoint, 0, months)
		for i := 1; i <= months; i++ {
			cash += u.MonthlyNet + int64(float64(spend)*(1-sc.spendFactor))
			inv = int64(float64(inv)*(1+sc.annualRet/12)) + u.SipMonthly
			owed -= min(owed, u.EmiMonthly)
			pts = append(pts, newPoint(ist.Midnight(now).AddDate(0, i, 0).Format("2006-01-02"), cash, inv, owed))
		}
		setChanges(pts, from.NetWorth)
		out = append(out, Scenario{Name: sc.name, Assumptions: sc.note, Points: pts})
	}
	return out
}

// NetWorth is the recorded month-end history over the last `months` months plus three projection scenarios for the next three.
func (s *Service) NetWorth(ctx context.Context, months int, now time.Time) (*NetWorthResponse, error) {
	r, err := ParsePeriod("month", "", "", now)
	if err != nil {
		return nil, err
	}
	d, err := s.Get(ctx, r, "month", now) // also records this month's row
	if err != nil {
		return nil, err
	}
	t := now.In(ist.Loc)
	start := time.Date(t.Year(), t.Month()-time.Month(months), 1, 0, 0, 0, 0, ist.Loc).Format("2006-01")
	cur, err := s.months.Find(ctx, bson.M{"_id": bson.M{"$gte": start}}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []monthDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	hist := make([]NWPoint, 0, len(docs))
	daily := s.dailyPoints(ctx, start+"-01", t.Format("2006-01-02"))
	for _, m := range docs {
		if len(daily) > 0 && m.Date >= daily[0].Date {
			break // the daily rows take over from the first day they cover
		}
		hist = append(hist, NWPoint{Date: m.Date, NetWorth: m.NetWorth, Owed: m.Owed, Cash: m.Cash, Investments: m.Investments})
	}
	hist = append(hist, daily...)
	setChanges(hist, 0)
	if len(hist) > 0 {
		hist[0].Change = 0
	}
	now0 := newPoint(t.Format("2006-01-02"), d.Position.Cash, d.Position.Current, d.Position.Liabilities)
	return &NetWorthResponse{History: hist, Scenarios: project(now0, d.Upcoming, now, 3), Method: projectionMethod}, nil
}
