package accounts

import (
	"context"
	"log"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Service struct {
	col *mongo.Collection
	txs *mongo.Collection // read for balances, link on create; owned by the transactions module
	// read for the account page's linked SIPs; owned by the funds module
	funds, fundSettings *mongo.Collection
}

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "bank", Value: 1}, {Key: "last4", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	if err == nil {
		normalizeBanks(ctx, col)
	}
	return &Service{col: col, txs: db.Collection("transactions"), funds: db.Collection("funds"), fundSettings: db.Collection("fund_settings")}, err
}

// normalizeBanks strips a "••1234" slug from banks saved with one. Safe to repeat. An account whose clean name would
// collide with another account on bank and last4 is left alone and logged.
func normalizeBanks(ctx context.Context, col *mongo.Collection) {
	cur, err := col.Find(ctx, bson.D{})
	if err != nil {
		log.Printf("normalize banks: %v", err)
		return
	}
	var all []Account
	if err := cur.All(ctx, &all); err != nil {
		log.Printf("normalize banks: %v", err)
		return
	}
	for _, a := range all {
		if c := CleanBank(a.Bank); c != a.Bank {
			if _, err := col.UpdateByID(ctx, a.ID, bson.M{"$set": bson.M{"bank": c}}); err != nil {
				log.Printf("normalize bank %q: %v", a.Bank, err)
			}
		}
	}
}

func (s *Service) Create(ctx context.Context, a Account) (*Account, error) {
	a.ID, a.CreatedAt = bson.NewObjectID(), time.Now().UTC()
	a.Name, a.Bank = strings.TrimSpace(a.Name), CleanBank(a.Bank)
	if a.OpeningAt.IsZero() {
		a.OpeningAt = a.CreatedAt
	}
	if _, err := s.col.InsertOne(ctx, a); err != nil {
		return nil, err
	}
	// Link transactions that were parsed before this account existed.
	_, err := s.txs.UpdateMany(ctx,
		bson.M{"account_last4": a.Last4, "account_id": bson.M{"$exists": false}, "card_id": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"account_id": a.ID}})
	return &a, err
}

// Patch holds the fields an account update may change; nil means leave as is.
type Patch struct {
	Name, Bank, Last4   *string
	OpeningBalancePaise *int64
	OpeningAt           *time.Time
}

// Update changes the given fields. Transactions already link by account id, so renaming the bank or last4 only affects how future SMS match.
func (s *Service) Update(ctx context.Context, id bson.ObjectID, p Patch) error {
	set := bson.M{}
	if p.Name != nil {
		set["name"] = strings.TrimSpace(*p.Name)
	}
	if p.Bank != nil {
		set["bank"] = CleanBank(*p.Bank)
	}
	if p.Last4 != nil {
		set["last4"] = *p.Last4
	}
	if p.OpeningBalancePaise != nil {
		set["opening_balance_paise"] = *p.OpeningBalancePaise
	}
	if p.OpeningAt != nil {
		set["opening_at"] = *p.OpeningAt
	}
	r, err := s.col.UpdateByID(ctx, id, bson.M{"$set": set})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

// Match finds the account an SMS refers to: by last4, narrowed by bank name if several share it.
func (s *Service) Match(ctx context.Context, bank, last4 string) (*Account, error) {
	if last4 == "" {
		return nil, nil
	}
	cur, err := s.col.Find(ctx, bson.M{"last4": last4})
	if err != nil {
		return nil, err
	}
	var all []Account
	if err := cur.All(ctx, &all); err != nil {
		return nil, err
	}
	if len(all) == 1 {
		return &all[0], nil
	}
	for i := range all {
		if strings.EqualFold(all[i].Bank, bank) {
			return &all[i], nil
		}
	}
	return nil, nil
}

// List returns accounts with balances: opening balance plus every linked transaction that arrived after it, credits adding and debits taking away.
// Arrival time (received_at) decides what counts, not the date in the SMS, which has no time of day and would drop same-day transactions.
// Direction decides the sign; older rows without it fall back to type.
func (s *Service) List(ctx context.Context) ([]View, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var accs []Account
	if err := cur.All(ctx, &accs); err != nil {
		return nil, err
	}
	neg := bson.M{"$multiply": bson.A{"$amount_paise", -1}}
	out := make([]View, len(accs))
	for i, a := range accs {
		out[i] = View{Account: a, BalancePaise: a.OpeningBalancePaise}
		c, err := s.txs.Aggregate(ctx, mongo.Pipeline{
			{{Key: "$match", Value: bson.M{"account_id": a.ID, "$expr": bson.M{"$gte": bson.A{bson.M{"$ifNull": bson.A{"$received_at", "$occurred_at"}}, a.OpeningAt}}}}},
			{{Key: "$group", Value: bson.M{"_id": nil, "net": bson.M{"$sum": bson.M{"$switch": bson.M{
				"branches": bson.A{
					bson.M{"case": bson.M{"$eq": bson.A{"$direction", "credit"}}, "then": "$amount_paise"},
					bson.M{"case": bson.M{"$eq": bson.A{"$direction", "debit"}}, "then": neg},
					bson.M{"case": bson.M{"$in": bson.A{"$type", bson.A{"income", "refund"}}}, "then": "$amount_paise"},
					bson.M{"case": bson.M{"$in": bson.A{"$type", bson.A{"expense", "atm", "emi", "card_bill_payment"}}}, "then": neg},
				},
				"default": 0,
			}}}}}},
		})
		if err != nil {
			return nil, err
		}
		var res []struct {
			Net int64 `bson:"net"`
		}
		if err := c.All(ctx, &res); err != nil {
			return nil, err
		}
		if len(res) > 0 {
			out[i].BalancePaise += res[0].Net
		}
	}
	return out, nil
}
