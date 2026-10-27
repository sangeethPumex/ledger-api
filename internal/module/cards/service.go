package cards

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/module/emis"
)

type Service struct {
	col  *mongo.Collection
	txs  *mongo.Collection // owned by the transactions module
	emis *mongo.Collection // owned by the emis module
}

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return &Service{col: col, txs: db.Collection("transactions"), emis: db.Collection(emis.Collection)}, err
}

// ErrShare is returned, with a message safe to show, when a limit cannot be shared as asked.
type ErrShare string

func (e ErrShare) Error() string { return string(e) }

// checkShare allows card `self` (nil for a card not yet made) to draw its limit from `owner`: the owner must exist and not share itself,
// and self must not already be the owner of a shared limit.
func (s *Service) checkShare(ctx context.Context, self *bson.ObjectID, owner bson.ObjectID) error {
	if self != nil && *self == owner {
		return ErrShare("a card cannot share its limit with itself")
	}
	var o Card
	switch err := s.col.FindOne(ctx, bson.M{"_id": owner}).Decode(&o); {
	case err == mongo.ErrNoDocuments:
		return ErrShare("the card to share a limit with was not found")
	case err != nil:
		return err
	case o.SharedWith != nil:
		return ErrShare(o.Name + " already shares another card's limit; pick the card that owns it")
	}
	if self != nil {
		n, err := s.col.CountDocuments(ctx, bson.M{"shared_with": *self})
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrShare("other cards share this card's limit, so it cannot share another")
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, c Card) (*Card, error) {
	if c.SharedWith != nil {
		if err := s.checkShare(ctx, nil, *c.SharedWith); err != nil {
			return nil, err
		}
		c.LimitPaise = 0
	}
	c.ID, c.CreatedAt, c.Name = bson.NewObjectID(), time.Now().UTC(), strings.TrimSpace(c.Name)
	c.Bank, c.Last4 = strings.TrimSpace(c.Bank), strings.TrimSpace(c.Last4)
	if c.Kind == "" {
		c.Kind = KindCredit
	}
	if _, err := s.col.InsertOne(ctx, c); err != nil {
		return nil, err
	}
	if c.Last4 != "" {
		_, err := s.txs.UpdateMany(ctx,
			bson.M{"account_last4": c.Last4, "account_id": bson.M{"$exists": false}, "card_id": bson.M{"$exists": false}},
			bson.M{"$set": bson.M{"card_id": c.ID}})
		return &c, err
	}
	return &c, nil
}

// Update applies the patch. Empty strings and zero numbers clear an optional field; a negative outstanding clears the manual one.
func (s *Service) Update(ctx context.Context, id bson.ObjectID, p Patch) error {
	set, unset := bson.M{}, bson.M{}
	put := func(key string, v any, clear bool) {
		if clear {
			unset[key] = ""
		} else {
			set[key] = v
		}
	}
	if p.Name != nil {
		set["name"] = strings.TrimSpace(*p.Name)
	}
	if p.Kind != nil {
		set["kind"] = *p.Kind
	}
	if p.Bank != nil {
		b := strings.TrimSpace(*p.Bank)
		put("bank", b, b == "")
	}
	if p.Last4 != nil {
		l := strings.TrimSpace(*p.Last4)
		put("last4", l, l == "")
	}
	if p.LimitPaise != nil {
		put("limit_paise", *p.LimitPaise, *p.LimitPaise == 0)
	}
	if p.BillingDay != nil {
		put("billing_day", *p.BillingDay, *p.BillingDay == 0)
	}
	if p.DueDay != nil {
		put("due_day", *p.DueDay, *p.DueDay == 0)
	}
	if p.OutstandingPaise != nil {
		put("outstanding_paise", *p.OutstandingPaise, *p.OutstandingPaise < 0)
	}
	if p.SharedWith != nil {
		if *p.SharedWith == "" {
			unset["shared_with"] = ""
		} else {
			owner, _ := bson.ObjectIDFromHex(*p.SharedWith)
			if err := s.checkShare(ctx, &id, owner); err != nil {
				return err
			}
			set["shared_with"] = owner
			delete(set, "limit_paise") // the owner's limit applies instead
			unset["limit_paise"] = ""
		}
	}
	upd := bson.M{}
	if len(set) > 0 {
		upd["$set"] = set
	}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	if len(upd) == 0 {
		return mongo.ErrNoDocuments
	}
	r, err := s.col.UpdateByID(ctx, id, upd)
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) Match(ctx context.Context, bank, last4 string) (*Card, error) {
	if last4 == "" {
		return nil, nil
	}
	cur, err := s.col.Find(ctx, bson.M{"last4": last4})
	if err != nil {
		return nil, err
	}
	var all []Card
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

func (s *Service) List(ctx context.Context) ([]Card, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []Card{}
	return out, cur.All(ctx, &out)
}

// spent is the net spend on a card since a date: expenses and cash withdrawals, less refunds.
// Bill payments and EMI debits are not new spends, so they are left out.
func (s *Service) spent(ctx context.Context, id bson.ObjectID, since time.Time) (net int64, n int, err error) {
	cur, err := s.txs.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"card_id": id, "occurred_at": bson.M{"$gte": since}, "type": bson.M{"$in": bson.A{"expense", "atm", "refund"}}}}},
		{{Key: "$group", Value: bson.M{"_id": "$type", "paise": bson.M{"$sum": "$amount_paise"}, "n": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return 0, 0, err
	}
	var rows []struct {
		Type  string `bson:"_id"`
		Paise int64  `bson:"paise"`
		N     int    `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, 0, err
	}
	for _, r := range rows {
		if r.Type == "refund" {
			net -= r.Paise
		} else {
			net += r.Paise
		}
		n += r.N
	}
	return net, n, nil
}

// Views lists every card with its cycle spend, limit use and linked loans.
func (s *Service) Views(ctx context.Context, now time.Time) ([]View, error) {
	cards, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	cur, err := s.emis.Find(ctx, bson.M{"cardId": bson.M{"$exists": true}})
	if err != nil {
		return nil, err
	}
	var linked []emis.Emi
	if err := cur.All(ctx, &linked); err != nil {
		return nil, err
	}
	out := make([]View, len(cards))
	for i, c := range cards {
		var mine []emis.Emi
		for _, e := range linked {
			if e.CardID != nil && *e.CardID == c.ID {
				mine = append(mine, e)
			}
		}
		spent, n, err := s.spent(ctx, c.ID, CycleStart(c, now))
		if err != nil {
			return nil, err
		}
		out[i] = Compute(c, spent, n, mine, now)
	}
	ApplyShared(out)
	return out, nil
}
