package snapshots

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /snapshots", auth(http.HandlerFunc(c.List)))
	mux.Handle("POST /snapshots/backfill", auth(http.HandlerFunc(c.Backfill)))
}
