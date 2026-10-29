package httpx

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestPageQuery(t *testing.T) {
	var p PageQuery
	if msg := p.Check(10, 100); msg != "" || p.Page != 1 || p.Limit != 10 || p.Skip() != 0 {
		t.Fatalf("defaults: %+v %q", p, msg)
	}
	p.Page, p.Limit = 3, 25
	if p.Skip() != 50 || Pages(51, 25) != 3 || Pages(0, 25) != 0 {
		t.Errorf("skip %d", p.Skip())
	}
	for _, bad := range []PageQuery{{Page: -1}, {Limit: 101}, {Search: string(make([]byte, 101))}} {
		if bad.Check(10, 100) == "" {
			t.Errorf("accepted %+v", bad)
		}
	}

	fields := map[string]string{"name": "name", "created": "_id"}
	def := bson.D{{Key: "name", Value: 1}}
	q := PageQuery{}
	if got, msg := q.SortBy(fields, def); msg != "" || len(got) != 1 {
		t.Errorf("default sort %v %q", got, msg)
	}
	q.Sort.Field, q.Sort.Dir = "name", "desc"
	got, _ := q.SortBy(fields, def)
	if len(got) != 2 || got[0].Value != -1 || got[1].Key != "_id" { // _id breaks ties so pages never overlap
		t.Errorf("sort %v", got)
	}
	q.Sort.Field = "password"
	if _, msg := q.SortBy(fields, def); msg == "" {
		t.Error("sorted by a field that is not allowed")
	}
}
