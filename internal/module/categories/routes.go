package categories

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("POST /categories", auth(http.HandlerFunc(c.Create)))
	mux.Handle("GET /categories", auth(http.HandlerFunc(c.List)))
}
