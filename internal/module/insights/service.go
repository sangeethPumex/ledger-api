// Package insights reads the transactions for patterns the user did not ask about: subscriptions, odd payments, when they spend.
package insights

import (
	"context"
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"golang.org/x/sync/errgroup"

	"ledger-backend/internal/httpx"
)

type Service struct {
	txs *mongo.Collection // owned by the transactions module; read only
}

func NewService(db *mongo.Database) *Service { return &Service{txs: db.Collection("transactions")} }

type Overview struct {
	Recurring    []Recurring `json:"recurring"`
	MonthlyPaise int64       `json:"monthly_paise"` // what the active recurring charges cost per month
	Anomalies    []Anomaly   `json:"anomalies"`
	Patterns     *Patterns   `json:"patterns"`
}

// Overview runs the vendor scan and the weekday/hour scan side by side; recurring and anomalies share the first, so the data is read once.
func (s *Service) Overview(ctx context.Context, now time.Time) (*Overview, error) {
	o := &Overview{}
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		all, err := s.series(gctx, now)
		if err != nil {
			return err
		}
		o.Recurring, o.Anomalies = detectRecurring(all, now), detectAnomalies(all, now)
		for _, r := range o.Recurring {
			if r.Status == "active" {
				o.MonthlyPaise += r.MonthlyPaise
			}
		}
		return nil
	})
	g.Go(func() (err error) { o.Patterns, err = s.patterns(gctx, now); return })
	return o, g.Wait()
}

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) Overview(w http.ResponseWriter, r *http.Request) {
	o, err := c.svc.Overview(r.Context(), time.Now())
	if err != nil {
		log.Printf("insights: %v", err)
		httpx.Err(w, 500, "could not compute insights")
		return
	}
	httpx.WriteJSON(w, 200, o)
}

// Patterns is the cheap half of Overview, for the dashboard heatmap.
func (c *Controller) Patterns(w http.ResponseWriter, r *http.Request) {
	p, err := c.svc.patterns(r.Context(), time.Now())
	if err != nil {
		log.Printf("insights: patterns: %v", err)
		httpx.Err(w, 500, "could not compute patterns")
		return
	}
	httpx.WriteJSON(w, 200, p)
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /insights", auth(http.HandlerFunc(c.Overview)))
	mux.Handle("GET /insights/patterns", auth(http.HandlerFunc(c.Patterns)))
}
