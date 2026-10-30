package categories

import "go.mongodb.org/mongo-driver/v2/bson"

const Collection = "categories"

// CardPayments is the category for paying a credit card's bill. A transaction in it names the card it paid.
const CardPayments = "Credit card payments"

type Category struct {
	ID   bson.ObjectID `bson:"_id" json:"id"`
	Name string        `bson:"name" json:"name"`
	// Icon is the key of an icon the web app draws (a short name such as "ForkKnife"). Only the key is stored, so it adds a few bytes to
	// a category; the app maps it to the picture and falls back to a plain tag for a key it does not know.
	Icon string `bson:"icon,omitempty" json:"icon,omitempty"`
}

// ValidIcon accepts an empty key (no icon) or a short alphanumeric one. Which keys exist is the app's business, not the database's.
func ValidIcon(k string) bool {
	if len(k) > 40 {
		return false
	}
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
