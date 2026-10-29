package transactions

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /summary", auth(http.HandlerFunc(c.Summary)))
	mux.Handle("GET /transactions", auth(http.HandlerFunc(c.List)))
	mux.Handle("POST /transactions/search", auth(http.HandlerFunc(c.Search)))
	mux.Handle("PATCH /transactions/{id}", auth(http.HandlerFunc(c.Patch)))
	mux.Handle("POST /raw-transactions/{id}/parse", auth(http.HandlerFunc(c.Reparse)))
}
