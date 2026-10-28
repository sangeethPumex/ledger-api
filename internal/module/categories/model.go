package categories

import "go.mongodb.org/mongo-driver/v2/bson"

const Collection = "categories"

// CardPayments is the category for paying a credit card's bill. A transaction in it names the card it paid.
const CardPayments = "Credit card payments"

type Category struct {
	ID   bson.ObjectID `bson:"_id" json:"id"`
	Name string        `bson:"name" json:"name"`
}
