// Package spend holds the pipeline stages that decide what counts as the user's spending, so budgets, insights and the dashboard agree.
package spend

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Match selects spending in [from, to): expenses, ATM withdrawals and EMIs going out. Transfers, refunds and card-bill payments never match.
func Match(from, to time.Time) bson.M {
	return bson.M{
		"occurred_at": bson.M{"$gte": from.UTC(), "$lt": to.UTC()},
		"type":        bson.M{"$in": bson.A{"expense", "atm", "emi"}},
		"direction":   bson.M{"$ne": "credit"},
	}
}

// Net leaves amount_paise as the user's own part: what a friend owes back is taken off. Documents with nothing left are dropped.
func Net() mongo.Pipeline {
	return mongo.Pipeline{
		{{Key: "$addFields", Value: bson.M{"amount_paise": bson.M{"$subtract": bson.A{"$amount_paise", bson.M{"$ifNull": bson.A{"$owed_paise", 0}}}}}}},
		{{Key: "$match", Value: bson.M{"amount_paise": bson.M{"$gt": 0}}}},
	}
}

// Parts is Net, then one document per category part: a split transaction fans out into its splits, any other keeps its own category.
// Afterwards category_id and amount_paise describe the part, not the whole payment.
func Parts() mongo.Pipeline {
	return append(Net(),
		bson.D{{Key: "$addFields", Value: bson.M{"_parts": bson.M{"$cond": bson.A{
			bson.M{"$gt": bson.A{bson.M{"$size": bson.M{"$ifNull": bson.A{"$splits", bson.A{}}}}, 0}},
			"$splits",
			bson.A{bson.M{"category_id": "$category_id", "paise": "$amount_paise"}},
		}}}}},
		bson.D{{Key: "$unwind", Value: "$_parts"}},
		bson.D{{Key: "$addFields", Value: bson.M{"category_id": "$_parts.category_id", "amount_paise": "$_parts.paise"}}},
	)
}
