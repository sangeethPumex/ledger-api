package habits

import (
	"log"
	"net/http"
	"time"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	out, err := c.svc.List(r.Context(), time.Now())
	if err != nil {
		log.Printf("grocery habits: %v", err)
		httpx.Err(w, 500, "could not load habits")
		return
	}
	httpx.WriteJSON(w, 200, out)
}

// Refresh recounts now, instead of waiting for the next scheduled run.
func (c *Controller) Refresh(w http.ResponseWriter, r *http.Request) {
	if err := c.svc.Refresh(r.Context(), time.Now()); err != nil {
		log.Printf("grocery habits refresh: %v", err)
		httpx.Err(w, 500, "could not refresh habits")
		return
	}
	c.List(w, r)
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /grocery/habits", auth(http.HandlerFunc(c.List)))
	mux.Handle("POST /grocery/habits/refresh", auth(http.HandlerFunc(c.Refresh)))
}
