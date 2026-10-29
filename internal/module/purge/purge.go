// Package purge drops everything dated before a cut-off day: transactions and their raw messages, recorded net-worth months and daily
// snapshots. Account balances are kept exactly: the movement being deleted is first folded into each account's opening balance.
package purge

import (
	"context"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/accounts"
)

type Service struct {
	db       *mongo.Database
	accounts *accounts.Service
}

func NewService(db *mongo.Database, a *accounts.Service) *Service { return &Service{db: db, accounts: a} }

// Fold is how an account's opening moves so its balance stays the same. Paise.
type Fold struct {
	Account    string `json:"account"`
	Opening    int64  `json:"openingPaise"`
	NewOpening int64  `json:"newOpeningPaise"`
	Folded     int64  `json:"foldedPaise"` // net movement of the deleted transactions
}

// Report is what was (or, without apply, would be) removed.
type Report struct {
	Before       string `json:"before"`
	Applied      bool   `json:"applied"`
	Transactions int64  `json:"transactions"`
	Raw          int64  `json:"rawMessages"`
	NetWorth     int64  `json:"netWorthMonths"`
	Snapshots    int64  `json:"snapshots"`
	Accounts     []Fold `json:"accounts"`
}

// Run removes everything before the IST day `before` (YYYY-MM-DD). Without apply it only counts.
func (s *Service) Run(ctx context.Context, before string, apply bool) (*Report, error) {
	cut, err := time.ParseInLocation("2006-01-02", before, ist.Loc)
	if err != nil {
		return nil, err
	}
	cutUTC := cut.UTC()
	rep := &Report{Before: before, Applied: apply, Accounts: []Fold{}}
	txs, raw := s.db.Collection("transactions"), s.db.Collection("raw_transactions")
	oldTx := bson.M{"received_at": bson.M{"$lt": cutUTC}}

	accs, err := s.accounts.Raw(ctx)
	if err != nil {
		return nil, err
	}
	var folds []mongo.WriteModel
	for _, a := range accs {
		if !a.OpeningAt.Before(cut) {
			continue
		}
		cur, err := txs.Aggregate(ctx, mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"account_id": a.ID}}},
			{{Key: "$addFields", Value: bson.M{"_at": bson.M{"$ifNull": bson.A{"$received_at", "$occurred_at"}}}}},
			{{Key: "$match", Value: bson.M{"_at": bson.M{"$gte": a.OpeningAt, "$lt": cutUTC}}}},
			{{Key: "$group", Value: bson.M{"_id": nil, "net": bson.M{"$sum": accounts.SignedExpr()}}}},
		})
		if err != nil {
			return nil, err
		}
		var res []struct {
			Net int64 `bson:"net"`
		}
		if err := cur.All(ctx, &res); err != nil {
			return nil, err
		}
		var net int64
		if len(res) > 0 {
			net = res[0].Net
		}
		rep.Accounts = append(rep.Accounts, Fold{Account: a.Name, Opening: a.OpeningBalancePaise, NewOpening: a.OpeningBalancePaise + net, Folded: net})
		folds = append(folds, mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": a.ID}).
			SetUpdate(bson.M{"$set": bson.M{"opening_balance_paise": a.OpeningBalancePaise + net, "opening_at": cutUTC}}))
	}

	monthCut := before[:7] // net-worth rows are keyed "2026-09"; the month holding the cut-off is kept
	counts := []struct {
		col *mongo.Collection
		q   bson.M
		to  *int64
	}{
		{txs, oldTx, &rep.Transactions},
		{raw, oldTx, &rep.Raw},
		{s.db.Collection("networth"), bson.M{"_id": bson.M{"$lt": monthCut}}, &rep.NetWorth},
		{s.db.Collection("daily_snapshots"), bson.M{"_id": bson.M{"$lt": before}}, &rep.Snapshots},
	}
	for _, c := range counts {
		if *c.to, err = c.col.CountDocuments(ctx, c.q); err != nil {
			return nil, err
		}
	}
	if !apply {
		return rep, nil
	}

	// The balance fold and the transaction delete commit together, or a failure could count the old movement twice.
	sess, err := s.db.Client().StartSession()
	if err != nil {
		return nil, err
	}
	defer sess.EndSession(ctx)
	if _, err = sess.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		if len(folds) > 0 {
			if _, err := s.db.Collection("accounts").BulkWrite(ctx, folds); err != nil {
				return nil, err
			}
		}
		_, err := txs.DeleteMany(ctx, oldTx)
		return nil, err
	}); err != nil {
		return nil, err
	}
	for _, c := range counts[1:] {
		if _, err := c.col.DeleteMany(ctx, c.q); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

// Purge is POST /maintenance/purge {before: "2026-10-01", apply: false}. It only reports what would go unless apply is true.
func (c *Controller) Purge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Before string `json:"before"`
		Apply  bool   `json:"apply"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if _, err := time.Parse("2006-01-02", req.Before); err != nil {
		httpx.Err(w, 400, "before must be a date like 2026-10-01")
		return
	}
	rep, err := c.svc.Run(r.Context(), req.Before, req.Apply)
	if err != nil {
		httpx.Err(w, 500, "could not purge")
		return
	}
	httpx.WriteJSON(w, 200, rep)
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("POST /maintenance/purge", auth(http.HandlerFunc(c.Purge)))
}
