package raw_transactions

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Service struct{ col *mongo.Collection }

func NewService(db *mongo.Database) *Service {
	return &Service{col: db.Collection(Collection)}
}

func (s *Service) Get(ctx context.Context, id bson.ObjectID) (*RawTransaction, error) {
	var rt RawTransaction
	if err := s.col.FindOne(ctx, bson.M{"_id": id}).Decode(&rt); err != nil {
		return nil, err
	}
	return &rt, nil
}

// Pending returns the oldest unparsed message ids, oldest first.
func (s *Service) Pending(ctx context.Context, limit int64) ([]bson.ObjectID, error) {
	cur, err := s.col.Find(ctx, bson.M{"status": StatusPending},
		options.Find().SetSort(bson.D{{Key: "received_at", Value: 1}}).SetLimit(limit).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	ids := make([]bson.ObjectID, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	return ids, nil
}

// RetryFailed puts every failed message back in the queue; called once at startup.
func (s *Service) RetryFailed(ctx context.Context) (int64, error) {
	r, err := s.col.UpdateMany(ctx, bson.M{"status": StatusFailed},
		bson.M{"$set": bson.M{"status": StatusPending}, "$unset": bson.M{"fail_reason": ""}})
	if err != nil {
		return 0, err
	}
	return r.ModifiedCount, nil
}

func (s *Service) SetStatus(ctx context.Context, id bson.ObjectID, status, reason string) error {
	upd := bson.M{"$set": bson.M{"status": status}}
	if reason != "" {
		upd["$set"] = bson.M{"status": status, "fail_reason": reason}
	} else {
		upd["$unset"] = bson.M{"fail_reason": ""}
	}
	_, err := s.col.UpdateByID(ctx, id, upd)
	return err
}
