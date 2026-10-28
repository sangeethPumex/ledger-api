package mandates

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Service struct {
	col *mongo.Collection
	txs *mongo.Collection // owned by the transactions module; read only, for detection
}

func NewService(db *mongo.Database) *Service {
	return &Service{col: db.Collection(Collection), txs: db.Collection("transactions")}
}

func (s *Service) all(ctx context.Context) ([]Mandate, error) {
	cur, err := s.col.Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	out := []Mandate{}
	return out, cur.All(ctx, &out)
}

// Plan is every mandate with its next date, the 12-month calendar and the totals, as of now.
func (s *Service) Plan(ctx context.Context, now time.Time) (Plan, error) {
	ms, err := s.all(ctx)
	if err != nil {
		return Plan{}, err
	}
	return BuildPlan(ms, now), nil
}

func (s *Service) Create(ctx context.Context, m Mandate) error {
	m.ID, m.CreatedAt = bson.NewObjectID(), time.Now().UTC()
	_, err := s.col.InsertOne(ctx, m)
	return err
}

func (s *Service) Update(ctx context.Context, id bson.ObjectID, m Mandate) error {
	set, unset := bson.M{"name": m.Name, "amount_paise": m.AmountPaise, "frequency": m.Frequency, "next_due": m.NextDue}, bson.M{}
	for k, v := range map[string]*bson.ObjectID{"account_id": m.AccountID, "category_id": m.CategoryID} {
		if v != nil {
			set[k] = *v
		} else {
			unset[k] = ""
		}
	}
	r, err := s.col.UpdateByID(ctx, id, bson.M{"$set": set, "$unset": unset})
	if err == nil && r.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	r, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	if err == nil && r.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return err
}

// Suggestions looks through the last 14 months of spending for payments that repeat at a regular interval and are not yet mandates.
func (s *Service) Suggestions(ctx context.Context, now time.Time) ([]Suggestion, error) {
	ms, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	known := make([]string, len(ms))
	for i, m := range ms {
		known[i] = m.Name
	}
	cur, err := s.txs.Find(ctx, bson.M{"type": "expense", "direction": bson.M{"$ne": "credit"}, "occurred_at": bson.M{"$gte": now.AddDate(0, -14, 0).UTC()}},
		options.Find().SetProjection(bson.M{"raw_vendor": 1, "upi_id": 1, "amount_paise": 1, "occurred_at": 1}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		RawVendor string    `bson:"raw_vendor"`
		UPIID     string    `bson:"upi_id"`
		Paise     int64     `bson:"amount_paise"`
		At        time.Time `bson:"occurred_at"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	samples := make([]Sample, 0, len(docs))
	for _, d := range docs {
		label := d.RawVendor
		if label == "" {
			label = d.UPIID
		}
		samples = append(samples, Sample{label, d.Paise, d.At})
	}
	return Detect(samples, known, now), nil
}
