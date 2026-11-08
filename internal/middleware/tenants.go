package middleware

import (
	"crypto/subtle"
	"net/http"
)

// Tenant is one user's handler, reached by that user's API key.
type Tenant struct {
	Key     string
	Handler http.Handler
}

// ByKey sends each request to the tenant whose key matches X-API-Key; no match is a 401.
func ByKey(ts []Tenant) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("X-API-Key"))
		var hit http.Handler
		for _, t := range ts { // no early exit, so timing does not reveal which key was close
			if subtle.ConstantTimeCompare(got, []byte(t.Key)) == 1 {
				hit = t.Handler
			}
		}
		if hit == nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		hit.ServeHTTP(w, r)
	})
}
