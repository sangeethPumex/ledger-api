package basket

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"ledger-backend/internal/httpx"
	"ledger-backend/internal/module/grocery-pantry-tracker/swiggy"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

// Get is the suggested basket from stored data; no Swiggy call.
func (c *Controller) Get(w http.ResponseWriter, r *http.Request) {
	out, err := c.svc.Preview(r.Context(), time.Now())
	if err != nil {
		log.Printf("grocery basket: %v", err)
		httpx.Err(w, 500, "could not build the basket")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

// Add puts the suggested basket in the Swiggy cart. Body: {"tipper": true} also adds the free-delivery tipper.
func (c *Controller) Add(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tipper bool `json:"tipper"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&in); err != nil {
			httpx.Err(w, 400, "bad request")
			return
		}
	}
	res, err := c.svc.Add(r.Context(), time.Now(), in.Tipper)
	switch {
	case errors.Is(err, swiggy.ErrNotConnected):
		httpx.Err(w, 409, "Connect Swiggy first")
	case errors.Is(err, ErrNothing):
		httpx.Err(w, 409, "Nothing is running low")
	case err != nil:
		log.Printf("grocery basket add: %v", err)
		httpx.Err(w, 502, "could not update the Swiggy cart")
	default:
		httpx.WriteJSON(w, 200, res)
	}
}

// Refresh rebuilds the pantry now, then returns the basket.
func (c *Controller) Refresh(w http.ResponseWriter, r *http.Request) {
	if err := c.svc.Refresh(r.Context(), time.Now()); err != nil && !errors.Is(err, swiggy.ErrNotConnected) {
		log.Printf("grocery basket refresh: %v", err)
		httpx.Err(w, 500, "could not refresh the pantry")
		return
	}
	c.Get(w, r)
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /grocery/basket", auth(http.HandlerFunc(c.Get)))
	mux.Handle("POST /grocery/basket", auth(http.HandlerFunc(c.Add)))
	mux.Handle("POST /grocery/basket/refresh", auth(http.HandlerFunc(c.Refresh)))
}
