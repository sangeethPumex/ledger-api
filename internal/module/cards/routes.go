package cards

import "net/http"

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("POST /cards", auth(http.HandlerFunc(c.Create)))
	mux.Handle("PATCH /cards/{id}", auth(http.HandlerFunc(c.Update)))
	mux.Handle("GET /cards/{id}/payments", auth(http.HandlerFunc(c.Payments)))
	mux.Handle("POST /cards/{id}/payments", auth(http.HandlerFunc(c.RecordPayment)))
	mux.Handle("DELETE /cards/{id}/payments/{pid}", auth(http.HandlerFunc(c.DeletePayment)))
	mux.Handle("GET /cards/{id}/activity", auth(http.HandlerFunc(c.Activity)))
	mux.Handle("GET /cards", auth(http.HandlerFunc(c.List)))
}
