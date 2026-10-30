package alerts

import (
	"log"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	out, err := c.svc.List(r.Context(), time.Now())
	if err != nil {
		log.Printf("list alerts: %v", err)
		httpx.Err(w, 500, "could not list alerts")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var in Input
	if !httpx.Decode(w, r, &in) {
		return
	}
	if msg := in.Validate(); msg != "" {
		httpx.Err(w, 400, msg)
		return
	}
	a, price, err := c.svc.Create(r.Context(), in, time.Now())
	switch {
	case err == errUnknown:
		httpx.Err(w, 404, err.Error())
	case err != nil:
		log.Printf("create alert: %v", err)
		httpx.Err(w, 400, err.Error())
	default:
		httpx.WriteJSON(w, 201, View{Alert: *a, Current: &price})
	}
}

func (c *Controller) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathID(w, r)
	if !ok {
		return
	}
	switch err := c.svc.Delete(r.Context(), id); {
	case err == mongo.ErrNoDocuments:
		httpx.Err(w, 404, "alert not found")
	case err != nil:
		httpx.Err(w, 500, "could not delete alert")
	default:
		w.WriteHeader(204)
	}
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	h := func(f http.HandlerFunc) http.Handler { return auth(f) }
	mux.Handle("GET /alerts", h(c.List))
	mux.Handle("POST /alerts", h(c.Create))
	mux.Handle("DELETE /alerts/{id}", h(c.Delete))
}
