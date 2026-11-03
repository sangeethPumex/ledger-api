package vehicles

import (
	"context"
	"io"
	"os"
	"testing"
	"time"
)

// Round-trips a file through the real bucket. Needs S3_TEST_BUCKET and AWS credentials. Skipped otherwise.
func TestStoreRoundTrip(t *testing.T) {
	bucket := os.Getenv("S3_TEST_BUCKET")
	if bucket == "" {
		t.Skip("S3_TEST_BUCKET not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := NewStore(ctx, bucket)
	if err != nil {
		t.Fatal(err)
	}
	key := "vehicles/_selftest/" + time.Now().Format("20060102150405") + ".txt"
	if err := s.Put(ctx, key, "text/plain", []byte("garage")); err != nil {
		t.Fatal(err)
	}
	body, err := s.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if err := s.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if string(got) != "garage" {
		t.Fatalf("got %q", got)
	}
}
