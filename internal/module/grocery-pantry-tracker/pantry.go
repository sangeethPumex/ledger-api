// Package pantry is the Grocery & Pantry Tracker. Its features live in sub-modules:
//   - swiggy: the Swiggy MCP link (OAuth sign-in, tool calls)
//   - orders: Food and Instamart orders and the month's spend
//   - habits: repeat Food orders, and the copilot nudge before one
//   - linker: matches Instamart orders to bank transactions and splits each payment by item category
//   - basket: Instamart items probably running low, one tap into the Swiggy cart
package pantry

import (
	"context"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/module/grocery-pantry-tracker/basket"
	"ledger-backend/internal/module/grocery-pantry-tracker/habits"
	"ledger-backend/internal/module/grocery-pantry-tracker/linker"
	"ledger-backend/internal/module/grocery-pantry-tracker/orders"
	"ledger-backend/internal/module/grocery-pantry-tracker/swiggy"
)

// Chatter is the LLM call (nim.Client satisfies it); it names habits and matches item names.
type Chatter interface {
	Chat(ctx context.Context, system, user string) (string, error)
}

// Register wires the tracker over one user's database and starts the background jobs. It returns the habits service for the copilot to nudge from.
// redirect is where Swiggy sends the browser after sign-in: the frontend's /grocery/callback page.
func Register(ctx context.Context, mux *http.ServeMux, db *mongo.Database, redirect string, auth func(http.Handler) http.Handler, llm Chatter, txs linker.Transactions, cats linker.Categories) (*habits.Service, error) {
	link := swiggy.NewAuth(db, redirect)
	client := swiggy.NewClient(link)
	ordSvc, err := orders.NewService(ctx, db, client)
	if err != nil {
		return nil, err
	}
	linkSvc := linker.NewService(db, ordSvc, client, llm, txs, cats)
	ordSvc.AfterSync = linkSvc.Run                   // match orders to bank transactions and split them, after every sync
	go ordSvc.Run(context.Background(), 6*time.Hour) // well inside Instamart's 15-day window

	habSvc := habits.NewService(db, ordSvc, client, llm)
	basSvc := basket.NewService(db, ordSvc, client, llm)
	go habSvc.Run(context.Background(), 6*time.Hour)
	go basSvc.Run(context.Background(), 6*time.Hour)

	swiggy.RegisterRoutes(mux, swiggy.NewController(link), auth)
	orders.RegisterRoutes(mux, orders.NewController(ordSvc), auth)
	habits.RegisterRoutes(mux, habits.NewController(habSvc), auth)
	basket.RegisterRoutes(mux, basket.NewController(basSvc), auth)
	return habSvc, nil
}
