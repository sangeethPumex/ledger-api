package insights

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/spend"
)

type point struct {
	ID     bson.ObjectID `bson:"id"`
	At     time.Time     `bson:"at"`
	Paise  int64         `bson:"paise"`
	Review bool          `bson:"review"`
}

// vendorSeries is every spend at one vendor, oldest first. Saved vendors group by id; unknown ones by their raw payee or UPI id.
type vendorSeries struct {
	Key        string         `bson:"_id"`
	VendorID   *bson.ObjectID `bson:"vendor_id"`
	CategoryID *bson.ObjectID `bson:"cat"`
	Name       string         `bson:"name"`
	Txns       []point        `bson:"txns"`
}

const seriesDays = 400 // a year of history plus slack, so a yearly charge shows up twice

// series groups the last seriesDays of spending per vendor in one pipeline: the match rides the occurred_at index, the sort keeps each
// vendor's points in time order, and the name lookup runs once per vendor rather than per transaction.
func (s *Service) series(ctx context.Context, now time.Time) ([]vendorSeries, error) {
	str := func(path string) bson.M { return bson.M{"$ifNull": bson.A{path, ""}} }
	filled := func(path string) bson.M { return bson.M{"$gt": bson.A{bson.M{"$strLenCP": str(path)}, 0}} }
	pipe := append(mongo.Pipeline{{{Key: "$match", Value: spend.Match(now.AddDate(0, 0, -seriesDays), now.Add(time.Hour))}}}, spend.Net()...)
	pipe = append(pipe, mongo.Pipeline{
		{{Key: "$sort", Value: bson.M{"occurred_at": 1}}},
		{{Key: "$addFields", Value: bson.M{"_key": bson.M{"$cond": bson.A{
			bson.M{"$ifNull": bson.A{"$vendor_id", false}}, bson.M{"$toString": "$vendor_id"},
			bson.M{"$toLower": bson.M{"$cond": bson.A{filled("$raw_vendor"), "$raw_vendor", str("$upi_id")}}},
		}}}}},
		{{Key: "$group", Value: bson.M{
			"_id":       "$_key",
			"vendor_id": bson.M{"$first": "$vendor_id"},
			"cat":       bson.M{"$last": "$category_id"},
			"raw":       bson.M{"$last": "$raw_vendor"},
			"upi":       bson.M{"$last": "$upi_id"},
			"txns":      bson.M{"$push": bson.M{"id": "$_id", "at": "$occurred_at", "paise": "$amount_paise", "review": bson.M{"$ifNull": bson.A{"$needs_review", false}}}},
		}}},
		{{Key: "$lookup", Value: bson.M{"from": "vendors", "localField": "vendor_id", "foreignField": "_id", "as": "ven"}}},
		{{Key: "$addFields", Value: bson.M{"name": bson.M{"$ifNull": bson.A{bson.M{"$first": "$ven.name"},
			bson.M{"$cond": bson.A{filled("$raw"), "$raw", bson.M{"$cond": bson.A{filled("$upi"), "$upi", "Unknown"}}}}}}}}},
		{{Key: "$project", Value: bson.M{"ven": 0, "raw": 0, "upi": 0}}},
	}...)
	cur, err := s.txs.Aggregate(ctx, pipe)
	if err != nil {
		return nil, err
	}
	var out []vendorSeries
	return out, cur.All(ctx, &out)
}
