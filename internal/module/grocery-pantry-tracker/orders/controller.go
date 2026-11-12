package orders

import (
	"errors"
	"log"
	"net/http"
	"time"

	"ledger-backend/internal/httpx"
	"ledger-backend/internal/ist"
	"ledger-backend/internal/module/grocery-pantry-tracker/swiggy"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

// List is one month's orders and spend; ?month=YYYY-MM, this IST month when left out.
func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	start := time.Now().In(ist.Loc)
	if m := r.URL.Query().Get("month"); m != "" {
		t, err := time.ParseInLocation("2006-01", m, ist.Loc)
		if err != nil {
			httpx.Err(w, 400, "month must be YYYY-MM")
			return
		}
		start = t
	}
	out, err := c.svc.Month(r.Context(), start)
	if err != nil {
		log.Printf("grocery orders: %v", err)
		httpx.Err(w, 500, "could not load orders")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

// Sync pulls the latest orders from Swiggy now. errors lists a source that failed while the other still synced.
func (c *Controller) Sync(w http.ResponseWriter, r *http.Request) {
	n, errs, err := c.svc.Sync(r.Context())
	switch {
	case errors.Is(err, swiggy.ErrNotConnected):
		httpx.Err(w, 409, "Connect Swiggy first")
	case err != nil:
		log.Printf("grocery sync: %v", err)
		httpx.Err(w, 502, "could not sync Swiggy orders")
	default:
		httpx.WriteJSON(w, 200, map[string]any{"synced": n, "errors": errs})
	}
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /grocery/orders", auth(http.HandlerFunc(c.List)))
	mux.Handle("POST /grocery/orders/sync", auth(http.HandlerFunc(c.Sync)))
}
