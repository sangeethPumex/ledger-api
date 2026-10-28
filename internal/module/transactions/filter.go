package transactions

import (
	"math"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Filter narrows the transaction list. Zero values mean "any".
type Filter struct {
	NeedsReview *bool
	AccountID   *bson.ObjectID
	CategoryID  *bson.ObjectID
	Q           string    // text found in the vendor or UPI id, any case
	Type        string    // one of validTypes
	Direction   string    // debit | credit
	From, To    time.Time // IST days, To inclusive; zero when unset
	MinPaise    int64
	MaxPaise    int64 // 0 = no cap
	Owed        bool  // only payments someone owes back
	Limit       int64
}

// Query is the Mongo filter for f. Pure.
func (f Filter) Query() bson.M {
	q := bson.M{}
	if f.NeedsReview != nil {
		q["needs_review"] = *f.NeedsReview
	}
	if f.AccountID != nil {
		q["account_id"] = *f.AccountID
	}
	if f.CategoryID != nil {
		q["category_id"] = *f.CategoryID
	}
	if f.Q != "" {
		re := bson.Regex{Pattern: regexp.QuoteMeta(f.Q), Options: "i"}
		q["$or"] = bson.A{bson.M{"raw_vendor": re}, bson.M{"upi_id": re}}
	}
	if f.Type != "" {
		q["type"] = f.Type
	}
	if f.Direction != "" {
		q["direction"] = f.Direction
	}
	if !f.From.IsZero() || !f.To.IsZero() {
		r := bson.M{}
		if !f.From.IsZero() {
			r["$gte"] = f.From.UTC()
		}
		if !f.To.IsZero() {
			r["$lt"] = f.To.AddDate(0, 0, 1).UTC()
		}
		q["received_at"] = r
	}
	if f.MinPaise > 0 || f.MaxPaise > 0 {
		r := bson.M{}
		if f.MinPaise > 0 {
			r["$gte"] = f.MinPaise
		}
		if f.MaxPaise > 0 {
			r["$lte"] = f.MaxPaise
		}
		q["amount_paise"] = r
	}
	if f.Owed {
		q["owed_paise"] = bson.M{"$gt": 0}
	}
	return q
}

// ParseFilter reads the list's query string; msg is non-empty (and safe to show) when a value is bad.
func ParseFilter(v url.Values) (f Filter, msg string) {
	f.Limit = 50
	if s := v.Get("needs_review"); s != "" {
		b, err := strconv.ParseBool(s)
		if err != nil {
			return f, "needs_review must be true or false"
		}
		f.NeedsReview = &b
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 || n > 1000 {
			return f, "limit must be 1-1000"
		}
		f.Limit = n
	}
	for name, dst := range map[string]**bson.ObjectID{"account_id": &f.AccountID, "category_id": &f.CategoryID} {
		if s := v.Get(name); s != "" {
			id, err := bson.ObjectIDFromHex(s)
			if err != nil {
				return f, name + " is not a valid id"
			}
			*dst = &id
		}
	}
	if f.Q = v.Get("q"); len(f.Q) > 100 {
		return f, "q is too long"
	}
	if f.Type = v.Get("type"); f.Type != "" && !validTypes[f.Type] {
		return f, "type is not a known transaction type"
	}
	if f.Direction = v.Get("direction"); f.Direction != "" && f.Direction != "debit" && f.Direction != "credit" {
		return f, "direction must be debit or credit"
	}
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if s := v.Get(name); s != "" {
			t, err := time.ParseInLocation("2006-01-02", s, ist)
			if err != nil {
				return f, name + " must be a date like 2026-12-31"
			}
			*dst = t
		}
	}
	for name, dst := range map[string]*int64{"min": &f.MinPaise, "max": &f.MaxPaise} { // rupees in the URL
		if s := v.Get(name); s != "" {
			r, err := strconv.ParseFloat(s, 64)
			if err != nil || r < 0 || r > maxPaise/100 {
				return f, name + " must be an amount in rupees"
			}
			*dst = int64(math.Round(r * 100))
		}
	}
	f.Owed = v.Get("owed") == "true"
	return f, ""
}
