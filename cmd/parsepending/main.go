// parsepending is a one-off: for each configured user, requeue transiently failed raw messages and parse everything pending.
// Run from ledger-backend (reads .env): go run ./cmd/parsepending
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/categories"
	"ledger-backend/internal/module/raw_transactions"
	"ledger-backend/internal/module/transactions"
	"ledger-backend/internal/module/vendors"
	"ledger-backend/internal/nim"
)

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func counts(ctx context.Context, db *mongo.Database) string {
	cur, err := db.Collection(raw_transactions.Collection).Aggregate(ctx, mongo.Pipeline{
		{{Key: "$group", Value: bson.M{"_id": bson.M{"s": "$status", "r": "$fail_reason"}, "n": bson.M{"$sum": 1}}}}})
	check(err)
	var rows []struct {
		ID struct{ S, R string } `bson:"_id"`
		N  int
	}
	check(cur.All(ctx, &rows))
	out := ""
	for _, r := range rows {
		out += fmt.Sprintf(" [%s/%s]=%d", r.ID.S, r.ID.R, r.N)
	}
	return out
}

func main() {
	_ = godotenv.Load(".env")
	model := os.Getenv("GEMINI_MODEL")
	llm := nim.New("https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", os.Getenv("GEMINI_API_KEY"), model)
	llm.MinGap = nim.GapFromEnv("GEMINI_MIN_GAP_SECONDS")
	ctx := context.Background()
	for _, p := range strings.Split(strings.ToUpper(os.Getenv("PEOPLE")+",ME"), ",") {
		p = strings.TrimSpace(p)
		if os.Getenv(p+"_SECRET") == "" {
			continue
		}
		client, err := mongo.Connect(options.Client().ApplyURI(os.Getenv(p + "_MONGODB_URI")))
		check(err)
		db := client.Database(os.Getenv(p + "_MONGODB_DB"))
		log.Printf("%s before:%s", p, counts(ctx, db))

		raw := raw_transactions.NewService(db)
		if n, err := raw.RetryTransient(ctx); err != nil {
			log.Printf("%s retry transient: %v", p, err)
		} else {
			log.Printf("%s requeued %d transient failures", p, n)
		}
		cats, err := categories.NewService(ctx, db)
		check(err)
		vend, err := vendors.NewService(ctx, db)
		check(err)
		acc, err := accounts.NewService(ctx, db)
		check(err)
		cd, err := cards.NewService(ctx, db)
		check(err)
		tx, err := transactions.NewService(ctx, db, raw, vend, cats, acc, cd, llm, llm.Model)
		check(err)
		tx.Holder, tx.ParseHint = os.Getenv(p+"_HOLDER"), os.Getenv(p+"_PARSE_HINT")

		pctx, cancel := context.WithTimeout(ctx, 40*time.Minute)
		tx.ParsePending(pctx)
		cancel()
		log.Printf("%s after:%s", p, counts(ctx, db))
	}
}
