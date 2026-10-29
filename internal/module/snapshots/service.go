package snapshots

import (
	"context"
	"errors"
	"log"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/emis"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/profile"
	"ledger-backend/internal/module/stocks"
	"ledger-backend/internal/spend"
)

// maxHistoryDays is how far back the first backfill reaches.
const maxHistoryDays = 400

type Service struct {
	col      *mongo.Collection
	txs      *mongo.Collection // owned by the transactions module; read only
	accounts *accounts.Service
	cards    *cards.Service
	emis     *emis.Service
	funds    *funds.Service
	stocks   *stocks.Service
	profile  *profile.Service
}

func NewService(ctx context.Context, db *mongo.Database, a *accounts.Service, c *cards.Service, e *emis.Service, f *funds.Service, st *stocks.Service, p *profile.Service) (*Service, error) {
	return &Service{col: db.Collection(Collection), txs: db.Collection("transactions"), accounts: a, cards: c, emis: e, funds: f, stocks: st, profile: p}, nil
}

// dayExpr is the IST date of a time field, as a Mongo expression.
func dayExpr(field string) bson.M {
	return bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": field, "timezone": "Asia/Kolkata"}}
}

type total struct {
	paise int64
	n     int
}

// sumByDay groups the documents the pipeline yields by IST day of occurred_at: their amount_paise and how many there were.
func (s *Service) sumByDay(ctx context.Context, pipe mongo.Pipeline) (map[string]total, error) {
	pipe = append(pipe, bson.D{{Key: "$group", Value: bson.M{"_id": dayExpr("$occurred_at"), "paise": bson.M{"$sum": "$amount_paise"}, "n": bson.M{"$sum": 1}}}})
	cur, err := s.txs.Aggregate(ctx, pipe)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Day   string `bson:"_id"`
		Paise int64  `bson:"paise"`
		N     int    `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]total, len(rows))
	for _, r := range rows {
		out[r.Day] = total{r.Paise, r.N}
	}
	return out, nil
}

type activity struct{ spent, income, card map[string]total }

// activity totals spending, income and card-bill payments per day over [from, to). Three aggregations, however many days.
func (s *Service) activity(ctx context.Context, from, to time.Time) (*activity, error) {
	window := bson.M{"$gte": from.UTC(), "$lt": to.UTC()}
	var a activity
	var err error
	if a.spent, err = s.sumByDay(ctx, append(mongo.Pipeline{{{Key: "$match", Value: spend.Match(from, to)}}}, spend.Net()...)); err != nil {
		return nil, err
	}
	if a.income, err = s.sumByDay(ctx, mongo.Pipeline{{{Key: "$match", Value: bson.M{"occurred_at": window, "type": "income", "direction": bson.M{"$ne": "debit"}}}}}); err != nil {
		return nil, err
	}
	if a.card, err = s.sumByDay(ctx, mongo.Pipeline{{{Key: "$match", Value: bson.M{"occurred_at": window, "type": "card_bill_payment", "direction": bson.M{"$ne": "credit"}}}}}); err != nil {
		return nil, err
	}
	return &a, nil
}

// deltas is each account's net movement per IST day (by arrival time) before `to`, counting only transactions on or after the account's
// opening, the same rule as the live balance.
func (s *Service) deltas(ctx context.Context, accs []accounts.Account, to time.Time) (map[bson.ObjectID]map[string]int64, error) {
	out := map[bson.ObjectID]map[string]int64{}
	if len(accs) == 0 {
		return out, nil
	}
	ids, opened := make(bson.A, len(accs)), make(bson.A, len(accs))
	for i, a := range accs {
		ids[i] = a.ID
		opened[i] = bson.M{"account_id": a.ID, "_at": bson.M{"$gte": a.OpeningAt}}
	}
	cur, err := s.txs.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"account_id": bson.M{"$in": ids}}}},
		{{Key: "$addFields", Value: bson.M{"_at": bson.M{"$ifNull": bson.A{"$received_at", "$occurred_at"}}}}},
		{{Key: "$match", Value: bson.M{"_at": bson.M{"$lt": to.UTC()}, "$or": opened}}},
		{{Key: "$group", Value: bson.M{"_id": bson.M{"acc": "$account_id", "day": dayExpr("$_at")}, "net": bson.M{"$sum": accounts.SignedExpr()}}}},
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID struct {
			Acc bson.ObjectID `bson:"acc"`
			Day string        `bson:"day"`
		} `bson:"_id"`
		Net int64 `bson:"net"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if out[r.ID.Acc] == nil {
			out[r.ID.Acc] = map[string]int64{}
		}
		out[r.ID.Acc][r.ID.Day] = r.Net
	}
	return out, nil
}

func midnight(day string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", day, ist.Loc)
	return t
}

// history rebuilds the given consecutive days from the transactions. Exact for balances and spending; holdings are valued at cost.
func (s *Service) history(ctx context.Context, days []string) ([]Snapshot, error) {
	if len(days) == 0 {
		return nil, nil
	}
	accs, err := s.accounts.Raw(ctx)
	if err != nil {
		return nil, err
	}
	lots, err := s.stocks.Lots(ctx)
	if err != nil {
		return nil, err
	}
	fs, err := s.funds.All(ctx)
	if err != nil {
		return nil, err
	}
	prof, err := s.profile.Get(ctx)
	if err != nil {
		return nil, err
	}
	end := midnight(days[len(days)-1]).AddDate(0, 0, 1)
	first := midnight(days[0])
	monthStart := time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, ist.Loc) // so "spent this month" is right from the first day
	act, err := s.activity(ctx, monthStart, end)
	if err != nil {
		return nil, err
	}
	dl, err := s.deltas(ctx, accs, end)
	if err != nil {
		return nil, err
	}
	bal := Balances(days, accs, dl)

	other := Holding{Invested: toPaise(prof.Other.Invested), Value: toPaise(prof.Other.Current)}
	mfProfile := Holding{Invested: toPaise(prof.MF.Invested), Value: toPaise(prof.MF.Current)}
	var month int64 // spending since the 1st, walked day by day
	out := make([]Snapshot, 0, len(days))
	for _, d := range monthStartTo(monthStart, days) {
		if d[len(d)-2:] == "01" {
			month = 0
		}
		month += act.spent[d].paise
		if d < days[0] {
			continue
		}
		sn := Snapshot{
			Date: d, Accounts: bal[d], Stocks: StocksAt(lots, d), MF: FundsAt(fs, d), Other: other,
			Spent: act.spent[d].paise, SpentMonth: month, SpendTxns: act.spent[d].n, Income: act.income[d].paise, CardPayments: act.card[d].paise,
			Valuation: ValuedCost, Source: SourceBackfill, UpdatedAt: time.Now().UTC(),
		}
		if sn.Accounts == nil {
			sn.Accounts = []AccountBal{}
		}
		if len(fs) == 0 {
			sn.MF = mfProfile
		}
		if owed, ok, err := s.emis.OwedOn(ctx, d); err != nil {
			return nil, err
		} else if ok {
			p := toPaise(owed)
			sn.Owed = &p
		}
		sn.Seal()
		out = append(out, sn)
	}
	return out, nil
}

// monthStartTo lists every day from the month start through the last of days.
func monthStartTo(monthStart time.Time, days []string) []string {
	return DaysBetween(monthStart.Format("2006-01-02"), days[len(days)-1])
}

// live is today's snapshot from the current figures: real balances and market prices.
func (s *Service) live(ctx context.Context, now time.Time) (*Snapshot, error) {
	today := ist.Midnight(now)
	day := today.Format("2006-01-02")
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, ist.Loc)
	accs, err := s.accounts.List(ctx)
	if err != nil {
		return nil, err
	}
	port, err := s.stocks.Portfolio(ctx, now)
	if err != nil {
		return nil, err
	}
	fsum, err := s.funds.Summary(ctx, now)
	if err != nil {
		return nil, err
	}
	prof, err := s.profile.Get(ctx)
	if err != nil {
		return nil, err
	}
	esum, err := s.emis.Summary(ctx, now)
	if err != nil {
		return nil, err
	}
	cs, err := s.cards.Views(ctx, now)
	if err != nil {
		return nil, err
	}
	act, err := s.activity(ctx, monthStart, today.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}

	sn := &Snapshot{
		Date: day, Accounts: make([]AccountBal, 0, len(accs)),
		Stocks: Holding{Invested: toPaise(port.Summary.Invested), Value: toPaise(port.Summary.Value)},
		Other:  Holding{Invested: toPaise(prof.Other.Invested), Value: toPaise(prof.Other.Current)},
		Spent:  act.spent[day].paise, SpendTxns: act.spent[day].n, Income: act.income[day].paise, CardPayments: act.card[day].paise,
		Valuation: ValuedMarket, Source: SourceLive, UpdatedAt: time.Now().UTC(),
	}
	if fsum.Count > 0 {
		sn.MF = Holding{Invested: toPaise(fsum.Invested), Value: toPaise(fsum.Value)}
	} else {
		sn.MF = Holding{Invested: toPaise(prof.MF.Invested), Value: toPaise(prof.MF.Current)}
	}
	for _, a := range accs {
		sn.Accounts = append(sn.Accounts, AccountBal{ID: a.ID, Name: a.Name, Bank: a.Bank, Last4: a.Last4, Paise: a.BalancePaise})
	}
	for d, t := range act.spent { // everything from the 1st to today
		if d <= day {
			sn.SpentMonth += t.paise
		}
	}
	owed := toPaise(esum.Outstanding)
	sn.Owed = &owed
	var used int64
	for _, c := range cs {
		used += c.UsedPaise
	}
	sn.CardUsed = &used
	sn.Seal()
	return sn, nil
}

func (s *Service) put(ctx context.Context, snaps ...Snapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	ops := make([]mongo.WriteModel, len(snaps))
	for i, sn := range snaps {
		ops[i] = mongo.NewReplaceOneModel().SetFilter(bson.M{"_id": sn.Date}).SetReplacement(sn).SetUpsert(true)
	}
	_, err := s.col.BulkWrite(ctx, ops)
	return err
}

// FirstDay is the earliest day worth a row: the first transaction or account opening, no further back than maxHistoryDays. "" when there is no data.
func (s *Service) FirstDay(ctx context.Context, now time.Time) (string, error) {
	first := ""
	accs, err := s.accounts.Raw(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range accs {
		if d := Day(a.OpeningAt); first == "" || d < first {
			first = d
		}
	}
	var t struct {
		At time.Time `bson:"received_at"`
	}
	err = s.txs.FindOne(ctx, bson.D{}, options.FindOne().SetSort(bson.D{{Key: "received_at", Value: 1}}).SetProjection(bson.M{"received_at": 1})).Decode(&t)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return "", err
	}
	if err == nil && !t.At.IsZero() {
		if d := Day(t.At); first == "" || d < first {
			first = d
		}
	}
	if floor := Day(now.AddDate(0, 0, -maxHistoryDays)); first != "" && first < floor {
		first = floor
	}
	return first, nil
}

// Backfill writes the days from..to (never today or later: today is live) that have no row yet. With force it also rebuilds rows that
// came from an earlier backfill, but never a live one. Returns how many rows it wrote.
func (s *Service) Backfill(ctx context.Context, from, to string, force bool, now time.Time) (int, error) {
	if yday := Day(now.AddDate(0, 0, -1)); to == "" || to > yday {
		to = yday
	}
	days := DaysBetween(from, to)
	if len(days) == 0 {
		return 0, nil
	}
	cur, err := s.col.Find(ctx, bson.M{"_id": bson.M{"$gte": days[0], "$lte": days[len(days)-1]}}, options.Find().SetProjection(bson.M{"source": 1}))
	if err != nil {
		return 0, err
	}
	var have []Snapshot
	if err := cur.All(ctx, &have); err != nil {
		return 0, err
	}
	src := map[string]string{}
	for _, h := range have {
		src[h.Date] = h.Source
	}
	want := map[string]bool{}
	var first, last string
	for _, d := range days {
		if s, ok := src[d]; !ok || (force && s != SourceLive) {
			want[d] = true
			if first == "" {
				first = d
			}
			last = d
		}
	}
	if len(want) == 0 {
		return 0, nil
	}
	built, err := s.history(ctx, DaysBetween(first, last))
	if err != nil {
		return 0, err
	}
	var out []Snapshot
	for _, b := range built {
		if want[b.Date] {
			out = append(out, b)
		}
	}
	return len(out), s.put(ctx, out...)
}

// Finalize settles a finished day: balances and spending are rebuilt from the transactions (late messages and recategorising change them),
// while a live row keeps the market values it had.
func (s *Service) Finalize(ctx context.Context, day string) error {
	built, err := s.history(ctx, []string{day})
	if err != nil || len(built) == 0 {
		return err
	}
	sn := built[0]
	var old Snapshot
	switch err := s.col.FindOne(ctx, bson.M{"_id": day}).Decode(&old); {
	case err == nil && old.Source == SourceLive:
		sn.Stocks, sn.MF, sn.Other, sn.Owed, sn.CardUsed, sn.Valuation, sn.Source = old.Stocks, old.MF, old.Other, old.Owed, old.CardUsed, old.Valuation, old.Source
		sn.Seal()
	case err != nil && !errors.Is(err, mongo.ErrNoDocuments):
		return err
	}
	return s.put(ctx, sn)
}

// Tick keeps the table current: fills any missing past days, settles yesterday once, and rewrites today from live figures.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	yday := Day(now.AddDate(0, 0, -1))
	first, err := s.FirstDay(ctx, now)
	if err != nil {
		return err
	}
	if first != "" && first <= yday {
		if _, err := s.Backfill(ctx, first, yday, false, now); err != nil {
			return err
		}
		var y Snapshot
		if err := s.col.FindOne(ctx, bson.M{"_id": yday}).Decode(&y); err == nil && y.UpdatedAt.Before(ist.Midnight(now)) {
			if err := s.Finalize(ctx, yday); err != nil { // yesterday's last live write was before midnight: settle it once
				return err
			}
		}
	}
	sn, err := s.live(ctx, now)
	if err != nil {
		return err
	}
	return s.put(ctx, *sn)
}

// Run ticks now and then every interval until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		tctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := s.Tick(tctx, time.Now()); err != nil {
			log.Printf("snapshots: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Range lists the rows from..to inclusive, oldest first.
func (s *Service) Range(ctx context.Context, from, to string) ([]Snapshot, error) {
	cur, err := s.col.Find(ctx, bson.M{"_id": bson.M{"$gte": from, "$lte": to}}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	return out, cur.All(ctx, &out)
}
