package vendors

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("POST /vendors", auth(http.HandlerFunc(c.Create)))
	mux.Handle("GET /vendors", auth(http.HandlerFunc(c.List)))
	mux.Handle("PATCH /vendors/{id}", auth(http.HandlerFunc(c.Update)))
	mux.Handle("DELETE /vendors/{id}", auth(http.HandlerFunc(c.Delete)))
	mux.Handle("POST /vendors/{id}/aliases", auth(http.HandlerFunc(c.AddAlias)))
}
