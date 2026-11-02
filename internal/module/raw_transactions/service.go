package raw_transactions

import (
	"context"
	"log"
	"time"

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

// Messages returns the message text of each id in one query, keyed by id. Ids with no message are left out.
func (s *Service) Messages(ctx context.Context, ids []bson.ObjectID) (map[bson.ObjectID]string, error) {
	cur, err := s.col.Find(ctx, bson.M{"_id": bson.M{"$in": ids}}, options.Find().SetProjection(bson.M{"message": 1}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID      bson.ObjectID `bson:"_id"`
		Message string        `bson:"message"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make(map[bson.ObjectID]string, len(docs))
	for _, d := range docs {
		out[d.ID] = d.Message
	}
	return out, nil
}

// Pending returns the oldest unparsed message ids, oldest first.
func (s *Service) Pending(ctx context.Context, limit int64) ([]bson.ObjectID, error) {
	return s.ids(ctx, bson.M{"status": StatusPending}, limit)
}

// Unparsed is Pending plus the failed ones: everything a manual parse can still act on.
func (s *Service) Unparsed(ctx context.Context, limit int64) ([]bson.ObjectID, error) {
	return s.ids(ctx, bson.M{"status": bson.M{"$in": bson.A{StatusPending, StatusFailed}}}, limit)
}

func (s *Service) ids(ctx context.Context, filter bson.M, limit int64) ([]bson.ObjectID, error) {
	cur, err := s.col.Find(ctx, filter,
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

// WatchInserts calls onInsert for every new raw message, via a MongoDB change stream (needs a replica set / Atlas).
// It reconnects after errors and calls onInsert once per (re)connect, so anything inserted while the stream was down is caught up.
func (s *Service) WatchInserts(ctx context.Context, onInsert func()) {
	pipe := mongo.Pipeline{{{Key: "$match", Value: bson.M{"operationType": "insert"}}}}
	for ctx.Err() == nil {
		cs, err := s.col.Watch(ctx, pipe)
		if err != nil {
			log.Printf("raw watch: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}
		onInsert()
		for cs.Next(ctx) {
			onInsert()
		}
		log.Printf("raw watch ended: %v", cs.Err())
		cs.Close(context.Background())
		time.Sleep(5 * time.Second)
	}
}

// KeepFor is how long a raw message is kept; the parsed transaction outlives it.
const KeepFor = 5 * 24 * time.Hour

// Sweep deletes messages older than KeepFor every interval, until ctx ends. Pending ones are kept: they are not parsed yet.
func (s *Service) Sweep(ctx context.Context, interval time.Duration) {
	for {
		r, err := s.col.DeleteMany(ctx, bson.M{"received_at": bson.M{"$lt": time.Now().Add(-KeepFor)}, "status": bson.M{"$ne": StatusPending}})
		if err != nil {
			log.Printf("raw sweep: %v", err)
		} else if r.DeletedCount > 0 {
			log.Printf("raw sweep: removed %d messages", r.DeletedCount)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// Delete removes one message; a missing one is fine.
func (s *Service) Delete(ctx context.Context, id bson.ObjectID) error {
	_, err := s.col.DeleteOne(ctx, bson.M{"_id": id})
	return err
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
