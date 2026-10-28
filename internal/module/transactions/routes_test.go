package transactions

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The PATCH route must exist: a build without it answers 404 and the card picker in the UI fails.
func TestPatchRouteRegistered(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, NewController(nil), func(h http.Handler) http.Handler { return h })
	r := httptest.NewRequest("PATCH", "/transactions/6ae1e340f668c7e5141b9eeb", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Errorf("got %d, want 400 from the handler (404 means no route)", w.Code)
	}
}
