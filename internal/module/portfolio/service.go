package portfolio

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/sync/errgroup"

	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/stocks"
)

type Service struct {
	funds    *funds.Service
	stocks   *stocks.Service
	accounts *accounts.Service
	settings *mongo.Collection // the target mix, one document
	income   *mongo.Collection
	txs      *mongo.Collection // owned by transactions; read only, for credits the bank SMS already reported
}

const targetKey = "target"

func NewService(ctx context.Context, db *mongo.Database, f *funds.Service, st *stocks.Service, a *accounts.Service) (*Service, error) {
	s := &Service{f, st, a, db.Collection("portfolio_settings"), db.Collection("income"), db.Collection("transactions")}
	_, err := s.income.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "date", Value: -1}}})
	return s, err
}

// Overview is everything the portfolio page shows, from one read of each source, fetched concurrently.
type Overview struct {
	Allocation Allocation `json:"allocation"`
	Target     *Target    `json:"target,omitempty"`
	Returns    Returns    `json:"returns"`
	Sips       []SipCheck `json:"sips"`
}

func (s *Service) Overview(ctx context.Context, now time.Time) (*Overview, error) {
	var (
		fs      []funds.Fund
		p       *stocks.Portfolio
		accs    []accounts.View
		genius  *funds.GeniusSip
		target  *Target
		g, gctx = errgroup.WithContext(ctx)
	)
	g.Go(func() (err error) { fs, err = s.funds.All(gctx); return })
	g.Go(func() (err error) { p, err = s.stocks.Portfolio(gctx, now); return })
	g.Go(func() (err error) { accs, err = s.accounts.List(gctx); return })
	g.Go(func() (err error) { genius, err = s.funds.GeniusSip(gctx); return })
	g.Go(func() (err error) { target, err = s.Target(gctx); return })
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var cash float64
	for _, a := range accs {
		cash += float64(a.BalancePaise) / 100
	}
	return &Overview{
		Allocation: Allocate(fs, p.Summary.Value, cash, target),
		Target:     target,
		Returns:    Rates(fs, p, now),
		Sips:       SipHealth(fs, genius, now),
	}, nil
}

// Target is the saved mix, nil when none was set.
func (s *Service) Target(ctx context.Context) (*Target, error) {
	var t Target
	err := s.settings.FindOne(ctx, bson.M{"_id": targetKey}).Decode(&t)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	return &t, err
}

// SetTarget saves the mix; nil removes it.
func (s *Service) SetTarget(ctx context.Context, t *Target) error {
	if t == nil {
		_, err := s.settings.DeleteOne(ctx, bson.M{"_id": targetKey})
		return err
	}
	_, err := s.settings.ReplaceOne(ctx, bson.M{"_id": targetKey}, t, options.Replace().SetUpsert(true))
	return err
}

// creditRe matches the SMS narrations of a dividend or interest credit.
const creditRe = "dividend|interest|int\\.? ?(pd|paid|cr)"

// Income reads typed entries and matching income SMS from the start of last financial year. Both are summed in the database by month;
// only the 50 latest typed entries travel. A credit you both typed and received as an SMS counts twice, so type only what no SMS reports.
func (s *Service) Income(ctx context.Context, now time.Time) (*Income, error) {
	from := fyStart(now).AddDate(-1, 0, 0)
	var (
		manual, sms []row
		entries     []Entry
		g, gctx     = errgroup.WithContext(ctx)
	)
	all := func(dst any) func(*mongo.Cursor, error) error {
		return func(cur *mongo.Cursor, err error) error {
			if err != nil {
				return err
			}
			return cur.All(gctx, dst)
		}
	}
	g.Go(func() error {
		return all(&manual)(s.income.Aggregate(gctx, mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"date": bson.M{"$gte": from.Format("2006-01-02")}}}},
			{{Key: "$group", Value: bson.M{"_id": bson.M{"ym": bson.M{"$substrBytes": bson.A{"$date", 0, 7}}, "kind": "$kind"}, "total": bson.M{"$sum": "$amount"}}}},
			{{Key: "$project", Value: bson.M{"_id": 0, "ym": "$_id.ym", "kind": "$_id.kind", "total": 1}}},
		}))
	})
	g.Go(func() error {
		return all(&sms)(s.txs.Aggregate(gctx, mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"type": "income", "occurred_at": bson.M{"$gte": from}, "raw_vendor": bson.M{"$regex": creditRe, "$options": "i"}}}},
			{{Key: "$group", Value: bson.M{
				"_id": bson.M{
					"ym":   bson.M{"$dateToString": bson.M{"format": "%Y-%m", "date": "$occurred_at", "timezone": "+05:30"}},
					"kind": bson.M{"$cond": bson.A{bson.M{"$regexMatch": bson.M{"input": "$raw_vendor", "regex": "dividend", "options": "i"}}, "dividend", "interest"}},
				},
				"total": bson.M{"$sum": "$amount_paise"},
			}}},
			{{Key: "$project", Value: bson.M{"_id": 0, "ym": "$_id.ym", "kind": "$_id.kind", "total": bson.M{"$divide": bson.A{"$total", 100}}}}},
		}))
	})
	g.Go(func() error {
		return all(&entries)(s.income.Find(gctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "date", Value: -1}}).SetLimit(50)))
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	in := Summarize(append(manual, sms...), entries, now)
	if in.Entries == nil {
		in.Entries = []Entry{}
	}
	return &in, nil
}

func (s *Service) AddEntry(ctx context.Context, in EntryInput) (*Entry, error) {
	e := &Entry{ID: bson.NewObjectID(), Date: in.Date, Kind: in.Kind, Source: in.Source, Amount: in.Amount}
	_, err := s.income.InsertOne(ctx, e)
	return e, err
}

func (s *Service) DeleteEntry(ctx context.Context, id bson.ObjectID) error {
	r, err := s.income.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}
