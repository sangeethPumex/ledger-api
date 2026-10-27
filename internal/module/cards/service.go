package cards

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Service struct {
	col *mongo.Collection
	txs *mongo.Collection // owned by the transactions module
}

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return &Service{col: col, txs: db.Collection("transactions")}, err
}

func (s *Service) Create(ctx context.Context, c Card) (*Card, error) {
	c.ID, c.CreatedAt, c.Name = bson.NewObjectID(), time.Now().UTC(), strings.TrimSpace(c.Name)
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
