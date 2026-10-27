package cards

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const Collection = "credit_cards"

// Card is a credit card. Bank and Last4 are optional; without Last4 no SMS is linked to it.
type Card struct {
	ID        bson.ObjectID `bson:"_id" json:"id"`
	Name      string        `bson:"name" json:"name"`
	Bank      string        `bson:"bank,omitempty" json:"bank,omitempty"`
	Last4     string        `bson:"last4,omitempty" json:"last4,omitempty"`
	CreatedAt time.Time     `bson:"created_at" json:"created_at"`
}
