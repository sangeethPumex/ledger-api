package transactions

import (
	"net/url"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestParseFilter(t *testing.T) {
	f, msg := ParseFilter(url.Values{"q": {"swiggy (1)"}, "type": {"expense"}, "min": {"100.50"}, "from": {"2026-10-01"}, "to": {"2026-10-31"}, "owed": {"true"}})
	if msg != "" || f.Limit != 50 || f.MinPaise != 10050 || !f.Owed {
		t.Fatalf("%+v %q", f, msg)
	}
	q := f.Query()
	if re := q["$or"]; re == nil || q["type"] != "expense" {
		t.Errorf("query %v", q)
	}
	// "to" is inclusive: the range ends at the start of the next IST day.
	r := q["received_at"].(bson.M)
	if got := r["$lt"].(time.Time); !got.Equal(time.Date(2026, 10, 31, 18, 30, 0, 0, time.UTC)) {
		t.Errorf("31 Oct must be included, range ends %v", got)
	}
	for _, bad := range []url.Values{{"type": {"nope"}}, {"direction": {"sideways"}}, {"from": {"yesterday"}}, {"min": {"-1"}}, {"limit": {"0"}}, {"category_id": {"x"}}} {
		if _, msg := ParseFilter(bad); msg == "" {
			t.Errorf("accepted %v", bad)
		}
	}
	if q, _ := ParseFilter(url.Values{}); len(q.Query()) != 0 {
		t.Errorf("empty filter should match everything: %v", q.Query())
	}
}

func TestParseFilterStatus(t *testing.T) {
	f, msg := ParseFilter(url.Values{"status": {"attention"}, "q": {"x"}})
	if msg != "" {
		t.Fatal(msg)
	}
	q := f.Query()
	if q["$and"] == nil || q["$or"] == nil { // the status OR must not overwrite the search OR
		t.Errorf("both ORs expected: %v", q)
	}
	if f, _ := ParseFilter(url.Values{"status": {"uncategorised"}}); f.Query()["category_id"] == nil {
		t.Error("uncategorised must test for a missing category")
	}
	if _, msg := ParseFilter(url.Values{"status": {"new"}}); msg == "" {
		t.Error("unknown status accepted")
	}
}
