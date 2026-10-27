package categories

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Service struct{ col *mongo.Collection }

func NewService(ctx context.Context, db *mongo.Database) (*Service, error) {
	col := db.Collection(Collection)
	_, err := col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true),
	})
	return &Service{col: col}, err
}

func (s *Service) Create(ctx context.Context, name string) (*Category, error) {
	c := &Category{ID: bson.NewObjectID(), Name: strings.TrimSpace(name)}
	if _, err := s.col.InsertOne(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Service) Rename(ctx context.Context, id bson.ObjectID, name string) error {
	r, err := s.col.UpdateByID(ctx, id, bson.M{"$set": bson.M{"name": strings.TrimSpace(name)}})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) List(ctx context.Context) ([]Category, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []Category{}
	return out, cur.All(ctx, &out)
}
