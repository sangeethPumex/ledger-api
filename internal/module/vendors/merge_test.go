package vendors

import (
	"context"
	"os"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Needs a MongoDB replica set (multi-document transactions), e.g. MONGODB_TEST_URI=mongodb://localhost:27017/?replicaSet=rs0. Skipped otherwise.
func TestMerge(t *testing.T) {
	uri := os.Getenv("MONGODB_TEST_URI")
	if uri == "" {
		t.Skip("MONGODB_TEST_URI not set")
	}
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := client.Database("catalyst_vendors_test")
	defer db.Drop(ctx)
	s, err := NewService(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var resynced bool
	s.OnChange = func(context.Context) { resynced = true }

	into, err := s.Create(ctx, "Swiggy Instamart", nil, []string{"instamart@icici"})
	if err != nil {
		t.Fatal(err)
	}
	from, err := s.Create(ctx, "SWIGGY", nil, []string{"swiggy@icici"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Collection("transactions").InsertMany(ctx, []bson.M{{"vendor_id": from.ID}, {"vendor_id": from.ID}, {"vendor_id": into.ID}})

	if err := s.Merge(ctx, from.ID, from.ID); err != ErrInvalid {
		t.Errorf("merging into itself = %v", err)
	}
	resynced = false
	err = s.Merge(ctx, from.ID, into.ID)
	if err != nil && strings.Contains(err.Error(), "replica set") {
		t.Skip("MongoDB is not a replica set; transactions unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !resynced {
		t.Error("OnChange should run after a merge")
	}
	vs, _ := s.List(ctx)
	if len(vs) != 1 || vs[0].ID != into.ID {
		t.Fatalf("vendors after merge = %+v", vs)
	}
	for _, a := range []string{"instamart@icici", "swiggy@icici", "swiggy"} {
		if !contains(vs[0].Aliases, a) {
			t.Errorf("alias %q missing from %v", a, vs[0].Aliases)
		}
	}
	if n, _ := db.Collection("transactions").CountDocuments(ctx, bson.M{"vendor_id": into.ID}); n != 3 {
		t.Errorf("%d transactions on the target, want 3", n)
	}
	// A failed merge leaves everything as it was: the target does not exist.
	other, _ := s.Create(ctx, "Other", nil, []string{"other@upi"})
	if err := s.Merge(ctx, other.ID, bson.NewObjectID()); err == nil {
		t.Fatal("merging into a missing vendor should fail")
	}
	if vs, _ = s.List(ctx); len(vs) != 2 {
		t.Errorf("a failed merge must roll back the delete; vendors = %+v", vs)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
