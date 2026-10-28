package mandates

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /mandates", auth(http.HandlerFunc(c.List)))
	mux.Handle("GET /mandates/suggestions", auth(http.HandlerFunc(c.Suggestions)))
	mux.Handle("POST /mandates", auth(http.HandlerFunc(c.Create)))
	mux.Handle("PUT /mandates/{id}", auth(http.HandlerFunc(c.Update)))
	mux.Handle("DELETE /mandates/{id}", auth(http.HandlerFunc(c.Delete)))
}
