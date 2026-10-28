package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/middleware"
	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/categories"
	"ledger-backend/internal/module/dashboard"
	"ledger-backend/internal/module/emis"
	"ledger-backend/internal/module/funds"
	"ledger-backend/internal/module/mandates"
	"ledger-backend/internal/module/profile"
	"ledger-backend/internal/module/raw_transactions"
	"ledger-backend/internal/module/stocks"
	"ledger-backend/internal/module/transactions"
	"ledger-backend/internal/module/vendors"
	"ledger-backend/internal/nim"
)

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing env var %s", k)
	}
	return v
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	_ = godotenv.Load(".env") // real env vars win; missing file is fine
	uri, dbName, apiKey := mustEnv("MONGODB_URI"), mustEnv("MONGODB_DB"), mustEnv("API_SECRET")
	model := mustEnv("NVIDIA_MODEL")
	port := mustEnv("PORT")

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	check(err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	check(client.Ping(ctx, nil))

	db := client.Database(dbName)
	auth := middleware.APIKey(apiKey)
	rawSvc := raw_transactions.NewService(db)
	catSvc, err := categories.NewService(ctx, db)
	check(err)
	vendSvc, err := vendors.NewService(ctx, db)
	check(err)
	accSvc, err := accounts.NewService(ctx, db)
	check(err)
	cardSvc, err := cards.NewService(ctx, db)
	check(err)
	llm := nim.New("https://integrate.api.nvidia.com/v1/chat/completions", mustEnv("NVIDIA_API_KEY"), model)
	chatLLM := nim.New("https://cloud.olakrutrim.com/v1/chat/completions", mustEnv("KRUTRIM_API_KEY"), mustEnv("KRUTRIM_MODEL"))
	txSvc, err := transactions.NewService(ctx, db, rawSvc, vendSvc, catSvc, accSvc, cardSvc, llm, model)
	check(err)
	vendSvc.OnChange = txSvc.Resync
	emiSvc, err := emis.NewService(ctx, db)
	check(err)
	fundSvc, err := funds.NewService(ctx, db)
	check(err)
	yahooURL := os.Getenv("YAHOO_CHART_URL")
	if yahooURL == "" {
		yahooURL = "https://query1.finance.yahoo.com/v8/finance/chart"
	}
	stockSvc := stocks.NewService(db, stocks.NewYahoo(yahooURL))
	profSvc := profile.NewService(db)
	mandSvc := mandates.NewService(db)
	dashSvc := dashboard.NewService(db, accSvc, cardSvc, profSvc, emiSvc, fundSvc, mandSvc, chatLLM)

	// Parse messages as ledger-api inserts them (change stream); sequential, so one LLM call at a time.
	// The watcher also fires on every (re)connect, which catches up anything missed while we were down.
	trigger := make(chan struct{}, 1)
	go rawSvc.WatchInserts(context.Background(), func() {
		select {
		case trigger <- struct{}{}:
		default: // a pass is already queued; it will pick this one up too
		}
	})
	go func() {
		if n, err := rawSvc.RetryFailed(context.Background()); err != nil {
			log.Printf("retry failed: %v", err)
		} else if n > 0 {
			log.Printf("requeued %d failed messages", n)
		}
		for range trigger {
			pctx, pcancel := context.WithTimeout(context.Background(), 5*time.Minute)
			txSvc.ParsePending(pctx)
			pcancel()
		}
	}()

	mux := http.NewServeMux()
	categories.RegisterRoutes(mux, categories.NewController(catSvc), auth)
	vendors.RegisterRoutes(mux, vendors.NewController(vendSvc), auth)
	accounts.RegisterRoutes(mux, accounts.NewController(accSvc), auth)
	cards.RegisterRoutes(mux, cards.NewController(cardSvc), auth)
	transactions.RegisterRoutes(mux, transactions.NewController(txSvc), auth)
	profile.RegisterRoutes(mux, profile.NewController(profSvc), auth)
	emis.RegisterRoutes(mux, emis.NewController(emiSvc), auth)
	mandates.RegisterRoutes(mux, mandates.NewController(mandSvc), auth)
	funds.RegisterRoutes(mux, funds.NewController(fundSvc), auth)
	stocks.RegisterRoutes(mux, stocks.NewController(stockSvc), auth)
	dashboard.RegisterRoutes(mux, dashboard.NewController(dashSvc), auth)

	// Every endpoint lives under /finance/api, e.g. /finance/api/transactions.
	root := http.NewServeMux()
	root.Handle("/finance/api/", http.StripPrefix("/finance/api", mux))

	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, root))
}
