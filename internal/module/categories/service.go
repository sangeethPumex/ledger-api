package categories

import (
	"context"
	"regexp"
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
	if err == nil { // always present: the app links card payments through it
		_, err = col.UpdateOne(ctx, bson.M{"name": CardPayments}, bson.M{"$setOnInsert": bson.M{"_id": bson.NewObjectID()}}, options.UpdateOne().SetUpsert(true))
	}
	return &Service{col: col}, err
}

// CardPaymentsID is the id of the CardPayments category.
func (s *Service) CardPaymentsID(ctx context.Context) (bson.ObjectID, error) {
	var c Category
	err := s.col.FindOne(ctx, bson.M{"name": CardPayments}).Decode(&c)
	return c.ID, err
}

// Exists reports whether a category with this id is there.
func (s *Service) Exists(ctx context.Context, id bson.ObjectID) (bool, error) {
	n, err := s.col.CountDocuments(ctx, bson.M{"_id": id})
	return n > 0, err
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

// Page is one page of categories whose name contains search (any case), plus the number matching. The count runs while the rows load.
func (s *Service) Page(ctx context.Context, search string, sort bson.D, skip, limit int64) ([]Category, int64, error) {
	q := bson.M{}
	if search != "" {
		q["name"] = bson.Regex{Pattern: regexp.QuoteMeta(search), Options: "i"}
	}
	var total int64
	var cerr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		total, cerr = s.col.CountDocuments(ctx, q)
	}()
	out := []Category{}
	cur, err := s.col.Find(ctx, q, options.Find().SetSort(sort).SetSkip(skip).SetLimit(limit).SetCollation(&options.Collation{Locale: "en", Strength: 2}))
	if err == nil {
		err = cur.All(ctx, &out)
	}
	<-done
	if err == nil {
		err = cerr
	}
	return out, total, err
}

func (s *Service) List(ctx context.Context) ([]Category, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []Category{}
	return out, cur.All(ctx, &out)
}
