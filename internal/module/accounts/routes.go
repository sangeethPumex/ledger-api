package accounts

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("POST /accounts", auth(http.HandlerFunc(c.Create)))
	mux.Handle("PATCH /accounts/{id}", auth(http.HandlerFunc(c.Update)))
	mux.Handle("GET /accounts", auth(http.HandlerFunc(c.List)))
	mux.Handle("GET /accounts/{id}", auth(http.HandlerFunc(c.Detail)))
}
