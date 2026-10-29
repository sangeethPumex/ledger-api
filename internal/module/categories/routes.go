package categories

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("POST /categories", auth(http.HandlerFunc(c.Create)))
	mux.Handle("POST /categories/search", auth(http.HandlerFunc(c.Search)))
	mux.Handle("PATCH /categories/{id}", auth(http.HandlerFunc(c.Update)))
	mux.Handle("GET /categories", auth(http.HandlerFunc(c.List)))
}
