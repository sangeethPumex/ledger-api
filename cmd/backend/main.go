package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"ledger-backend/internal/middleware"
	"ledger-backend/internal/module/accounts"
	"ledger-backend/internal/module/alerts"
	"ledger-backend/internal/module/brainjogger"
	"ledger-backend/internal/module/budgets"
	"ledger-backend/internal/module/cards"
	"ledger-backend/internal/module/categories"
	"ledger-backend/internal/module/copilot"
	"ledger-backend/internal/module/dashboard"
	"ledger-backend/internal/module/debts"
	"ledger-backend/internal/module/emis"
	"ledger-backend/internal/module/funds"
	pantry "ledger-backend/internal/module/grocery-pantry-tracker"
	"ledger-backend/internal/module/insights"
	"ledger-backend/internal/module/mandates"
	"ledger-backend/internal/module/portfolio"
	"ledger-backend/internal/module/profile"
	"ledger-backend/internal/module/purge"
	"ledger-backend/internal/module/raw_transactions"
	"ledger-backend/internal/module/snapshots"
	"ledger-backend/internal/module/stocks"
	"ledger-backend/internal/module/transactions"
	"ledger-backend/internal/module/vehicles"
	"ledger-backend/internal/module/vendors"
	"ledger-backend/internal/module/views"
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
	_ = godotenv.Load(".env")        // real env vars win; missing file is fine
	model := mustEnv("GEMINI_MODEL") // gemini-3.1-flash-lite: 15 requests/min, 500/day, 250K tokens/min on the free tier
	port := mustEnv("PORT")
	// One budget for connecting every user's cluster and building their services (index creation); slow Atlas links need more than 10s.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Gemini (OpenAI-compatible endpoint) does both jobs: parsing messages into transactions and the dashboard chat.
	chatLLM := nim.New("https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", mustEnv("GEMINI_API_KEY"), model)
	yahooURL := os.Getenv("YAHOO_CHART_URL")
	if yahooURL == "" {
		yahooURL = "https://query1.finance.yahoo.com/v8/finance/chart"
	}
	// Vehicle papers and photos; without S3_BUCKET the garage works but file uploads answer 503.
	store, err := vehicles.NewStore(ctx, os.Getenv("S3_BUCKET"))
	check(err)

	// Every user gets their own cluster, database, services and background jobs; the API key picks which one answers.
	// A person is on when <NAME>_SECRET is set, and then needs <NAME>_MONGODB_URI, _MONGODB_DB and _HOLDER (_PARSE_HINT is optional). Add a name here for a third person.
	var users []user
	for _, p := range []string{"SANGEETH", "SILPA"} {
		if os.Getenv(p+"_SECRET") != "" {
			users = append(users, user{strings.ToLower(p), os.Getenv(p + "_SECRET"), mustEnv(p + "_MONGODB_URI"), mustEnv(p + "_MONGODB_DB"), mustEnv(p + "_HOLDER"), os.Getenv(p + "_PARSE_HINT")})
		}
	}
	if len(users) == 0 {
		log.Fatal("set SANGEETH_SECRET (and SILPA_SECRET) with their MONGODB_URI / MONGODB_DB / HOLDER")
	}
	seenName, seenKey := map[string]bool{}, map[string]bool{}
	var tenants []middleware.Tenant
	for _, u := range users {
		if u.Name == "" || u.APISecret == "" || u.MongoURI == "" || u.DB == "" || u.Holder == "" || seenName[u.Name] || seenKey[u.APISecret] {
			log.Fatalf("%q needs its own secret, mongodb_uri, db and holder (two people cannot share a secret)", u.Name)
		}
		seenName[u.Name], seenKey[u.APISecret] = true, true
		client, err := mongo.Connect(options.Client().ApplyURI(u.MongoURI))
		check(err)
		check(client.Ping(ctx, nil))
		tenants = append(tenants, middleware.Tenant{Key: u.APISecret, Handler: build(ctx, client.Database(u.DB), u, chatLLM, store, yahooURL)})
	}

	// Every endpoint lives under /finance/api, e.g. /finance/api/transactions.
	root := http.NewServeMux()
	root.Handle("/finance/api/", http.StripPrefix("/finance/api", middleware.ByKey(tenants)))

	log.Printf("listening on :%s for %d user(s)", port, len(users))
	log.Fatal(http.ListenAndServe(":"+port, root))
}

// user is one person's slice of the app: a cluster and database of their own and the key that reaches it.
// Holder is the name on their bank accounts (so own-account moves read as transfers); ParseHint is optional extra SMS-parsing rules.
type user struct{ Name, APISecret, MongoURI, DB, Holder, ParseHint string }

// build wires every module over one user's database and returns their API.
func build(ctx context.Context, db *mongo.Database, u user, chatLLM *nim.Client, store *vehicles.Store, yahooURL string) http.Handler {
	auth := middleware.APIKey(u.APISecret)
	rawSvc := raw_transactions.NewService(db)
	catSvc, err := categories.NewService(ctx, db)
	check(err)
	vendSvc, err := vendors.NewService(ctx, db)
	check(err)
	accSvc, err := accounts.NewService(ctx, db)
	check(err)
	cardSvc, err := cards.NewService(ctx, db)
	check(err)
	txSvc, err := transactions.NewService(ctx, db, rawSvc, vendSvc, catSvc, accSvc, cardSvc, chatLLM, chatLLM.Model)
	check(err)
	txSvc.Holder, txSvc.ParseHint = u.Holder, u.ParseHint
	vendSvc.OnChange = txSvc.Resync
	emiSvc, err := emis.NewService(ctx, db)
	check(err)
	if u.Name == "sangeeth" { // the car-loan statement is his; no one else's database gets it
		check(emiSvc.SeedCarLoan(ctx))
	}
	fundSvc, err := funds.NewService(ctx, db)
	check(err)
	stockSvc := stocks.NewService(db, stocks.NewYahoo(yahooURL))
	alertSvc, err := alerts.NewService(ctx, db, stockSvc, fundSvc)
	check(err)
	go alertSvc.Run(context.Background(), 5*time.Minute)
	portSvc, err := portfolio.NewService(ctx, db, fundSvc, stockSvc, accSvc)
	check(err)
	profSvc := profile.NewService(db)
	mandSvc := mandates.NewService(db)
	jogSvc := brainjogger.NewService(db)
	go jogSvc.Run(context.Background(), 30*time.Second)
	vehSvc, err := vehicles.NewService(ctx, db, store, jogSvc)
	check(err)
	go vehSvc.Run(context.Background(), time.Hour)
	budSvc, err := budgets.NewService(ctx, db)
	check(err)
	viewSvc, err := views.NewService(ctx, db)
	check(err)
	dashSvc := dashboard.NewService(db, accSvc, cardSvc, profSvc, emiSvc, fundSvc, stockSvc, mandSvc, chatLLM)
	snapSvc, err := snapshots.NewService(ctx, db, accSvc, cardSvc, emiSvc, fundSvc, stockSvc, profSvc)
	check(err)
	dashSvc.UseSnapshots(snapSvc)
	insSvc := insights.NewService(db)
	copSvc, err := copilot.NewService(ctx, db, accSvc, cardSvc, profSvc, emiSvc, fundSvc, mandSvc, budSvc, catSvc, insSvc, snapSvc, jogSvc)
	check(err)
	dashSvc.UseCopilot(copSvc)
	// Rules run every 15 minutes and whenever a parsed message lands.
	go copSvc.Run(context.Background(), 15*time.Minute)
	// One row per day (balances, holdings, net worth, spending): fills the past from the transactions, then keeps today's row live.
	go snapSvc.Run(context.Background(), 15*time.Minute)

	go rawSvc.Sweep(context.Background(), time.Hour) // raw messages go after 5 days; transactions stay

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
			copSvc.Poke()
			pcancel()
		}
	}()
	// The HTTP API is a thin layer over the services, with auth and routing.
	mux := http.NewServeMux()
	categories.RegisterRoutes(mux, categories.NewController(catSvc), auth)
	vendors.RegisterRoutes(mux, vendors.NewController(vendSvc), auth)
	debts.RegisterRoutes(mux, debts.NewController(debts.NewService(db)), auth)
	accounts.RegisterRoutes(mux, accounts.NewController(accSvc), auth)
	cards.RegisterRoutes(mux, cards.NewController(cardSvc), auth)
	transactions.RegisterRoutes(mux, transactions.NewController(txSvc), auth)
	profile.RegisterRoutes(mux, profile.NewController(profSvc), auth)
	emis.RegisterRoutes(mux, emis.NewController(emiSvc), auth)
	mandates.RegisterRoutes(mux, mandates.NewController(mandSvc), auth)
	funds.RegisterRoutes(mux, funds.NewController(fundSvc), auth)
	stocks.RegisterRoutes(mux, stocks.NewController(stockSvc), auth)
	portfolio.RegisterRoutes(mux, portfolio.NewController(portSvc), auth)
	alerts.RegisterRoutes(mux, alerts.NewController(alertSvc), auth)
	dashboard.RegisterRoutes(mux, dashboard.NewController(dashSvc), auth)
	brainjogger.RegisterRoutes(mux, brainjogger.NewController(jogSvc), auth)
	budgets.RegisterRoutes(mux, budgets.NewController(budSvc), auth)
	insights.RegisterRoutes(mux, insights.NewController(insSvc), auth)
	copilot.RegisterRoutes(mux, copilot.NewController(copSvc), auth)
	views.RegisterRoutes(mux, views.NewController(viewSvc), auth)
	snapshots.RegisterRoutes(mux, snapshots.NewController(snapSvc), auth)
	vehicles.RegisterRoutes(mux, vehicles.NewController(vehSvc), auth)
	purge.RegisterRoutes(mux, purge.NewController(purge.NewService(db, accSvc)), auth)
	// Grocery & Pantry Tracker. Swiggy sends the browser back to the frontend's callback page after sign-in (exact match, https or localhost).
	swiggyRedirect := os.Getenv("SWIGGY_REDIRECT_URI")
	if swiggyRedirect == "" {
		swiggyRedirect = "http://localhost:3000/grocery/callback"
	}
	habSvc, err := pantry.Register(ctx, mux, db, swiggyRedirect, auth, chatLLM, txSvc, catSvc)
	check(err)
	copSvc.UseHabits(habSvc) // the food-habit nudge reads what the grocery module found

	return mux
}
