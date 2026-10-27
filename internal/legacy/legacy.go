// Package legacy holds one-off data fixes run at startup.
package legacy

import (
	"context"
	"log"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// StringIDs re-keys documents whose _id is a string (written by an earlier build that let the browser pick ids) to ObjectIDs,
// which is what the emis and funds modules decode. The fields are identical, so nothing else changes. Safe to run on every start.
func StringIDs(ctx context.Context, col *mongo.Collection) error {
	cur, err := col.Find(ctx, bson.M{"_id": bson.M{"$type": "string"}})
	if err != nil {
		return err
	}
	var docs []bson.M
	if err := cur.All(ctx, &docs); err != nil {
		return err
	}
	for _, d := range docs {
		old := d["_id"]
		d["_id"] = bson.NewObjectID()
		if _, err := col.InsertOne(ctx, d); err != nil {
			return err
		}
		if _, err := col.DeleteOne(ctx, bson.M{"_id": old}); err != nil {
			return err
		}
	}
	if len(docs) > 0 {
		log.Printf("%s: re-keyed %d documents with string ids", col.Name(), len(docs))
	}
	return nil
}
