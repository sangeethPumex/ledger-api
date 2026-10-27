package cards

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("POST /cards", auth(http.HandlerFunc(c.Create)))
	mux.Handle("PATCH /cards/{id}", auth(http.HandlerFunc(c.Update)))
	mux.Handle("GET /cards", auth(http.HandlerFunc(c.List)))
}
