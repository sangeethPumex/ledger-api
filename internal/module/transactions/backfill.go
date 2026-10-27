package transactions

import (
	"context"
	"log"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/module/raw_transactions"
)

// backfillReceivedAt gives transactions parsed before received_at existed the arrival time of their raw message, in one aggregation.
// Idempotent: only documents still missing the field are touched.
func backfillReceivedAt(ctx context.Context, db *mongo.Database, col *mongo.Collection) error {
	pipe := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"received_at": bson.M{"$exists": false}}}},
		{{Key: "$lookup", Value: bson.M{"from": raw_transactions.Collection, "localField": "raw_id", "foreignField": "_id", "as": "raw"}}},
		{{Key: "$project", Value: bson.M{"received_at": bson.M{"$ifNull": bson.A{bson.M{"$first": "$raw.received_at"}, "$occurred_at"}}}}},
		{{Key: "$merge", Value: bson.M{"into": col.Name(), "on": "_id", "whenMatched": "merge", "whenNotMatched": "discard"}}},
	}
	cur, err := col.Aggregate(ctx, pipe)
	if err != nil {
		log.Printf("backfill received_at: %v", err)
		return nil // a cosmetic backfill must not stop the server
	}
	return cur.Close(ctx)
}
