package categories

import "go.mongodb.org/mongo-driver/v2/bson"

const Collection = "categories"

type Category struct {
	ID   bson.ObjectID `bson:"_id" json:"id"`
	Name string        `bson:"name" json:"name"`
}
