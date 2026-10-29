package snapshots

import (
	"log"
	"net/http"
	"time"

	"ledger-backend/internal/httpx"
	"ledger-backend/internal/ist"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

const maxRange = 400

func validDay(s string) bool {
	_, err := time.ParseInLocation("2006-01-02", s, ist.Loc)
	return err == nil
}

// List is GET /snapshots?from=YYYY-MM-DD&to=YYYY-MM-DD: the daily rows, oldest first. Without dates it returns the last 30 days.
func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if to == "" {
		to = Day(now)
	}
	if from == "" {
		from = Day(midnight(to).AddDate(0, 0, -29))
	}
	if !validDay(from) || !validDay(to) {
		httpx.Err(w, 400, "from and to must be dates like 2026-10-31")
		return
	}
	if from > to || len(DaysBetween(from, to)) > maxRange {
		httpx.Err(w, 400, "from must not be after to, and the range is at most 400 days")
		return
	}
	rows, err := c.svc.Range(r.Context(), from, to)
	if err != nil {
		log.Printf("snapshots: list: %v", err)
		httpx.Err(w, 500, "could not list snapshots")
		return
	}
	httpx.WriteJSON(w, 200, rows)
}

// Backfill is POST /snapshots/backfill {from, to, force}: builds the past days that have no row yet from the transactions. Dates default
// to the first day with data through yesterday. force rebuilds earlier backfilled rows too; live rows are never replaced.
func (c *Controller) Backfill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From  string `json:"from"`
		To    string `json:"to"`
		Force bool   `json:"force"`
	}
	if r.ContentLength != 0 && !httpx.Decode(w, r, &req) {
		return
	}
	now := time.Now()
	if req.From == "" {
		first, err := c.svc.FirstDay(r.Context(), now)
		if err != nil {
			httpx.Err(w, 500, "could not find the first day")
			return
		}
		req.From = first
	}
	if req.From == "" {
		httpx.WriteJSON(w, 200, map[string]int{"written": 0})
		return
	}
	if !validDay(req.From) || (req.To != "" && !validDay(req.To)) {
		httpx.Err(w, 400, "from and to must be dates like 2026-10-31")
		return
	}
	n, err := c.svc.Backfill(r.Context(), req.From, req.To, req.Force, now)
	if err != nil {
		log.Printf("snapshots: backfill: %v", err)
		httpx.Err(w, 500, "could not backfill snapshots")
		return
	}
	httpx.WriteJSON(w, 200, map[string]int{"written": n})
}
