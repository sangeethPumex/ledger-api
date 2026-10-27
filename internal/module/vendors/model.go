package vendors

import (
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const Collection = "vendors"

type Vendor struct {
	ID         bson.ObjectID  `bson:"_id" json:"id"`
	Name       string         `bson:"name" json:"name"`
	CategoryID *bson.ObjectID `bson:"category_id,omitempty" json:"category_id,omitempty"`
	// Aliases are lowercase UPI ids / merchant strings. A transaction matches
	// a vendor when any alias appears in its raw vendor, UPI id or SMS text.
	Aliases []string `bson:"aliases,omitempty" json:"aliases"`
}

func Normalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
