// Package raw_transactions is the backend's view of messages written by ledger-api (ingest).
// It never inserts or edits message bodies; it only reads them and sets the status.
package raw_transactions

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const Collection = "raw_transactions"

const (
	StatusPending = "pending"
	StatusParsed  = "parsed"
	StatusIgnored = "ignored"
	StatusFailed  = "failed"
)

type RawTransaction struct {
	ID         bson.ObjectID `bson:"_id" json:"id"`
	Message    string        `bson:"message" json:"message"`
	ReceivedAt time.Time     `bson:"received_at" json:"received_at"`
	Status     string        `bson:"status" json:"status"`
	FailReason string        `bson:"fail_reason,omitempty" json:"fail_reason,omitempty"`
}
