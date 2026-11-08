package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestByKey(t *testing.T) {
	say := func(s string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(s)) })
	}
	h := ByKey([]Tenant{{"a", say("A")}, {"b", say("B")}})
	for key, want := range map[string]string{"a": "A", "b": "B", "c": "", "": ""} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if want == "" && w.Code != 401 || want != "" && w.Body.String() != want {
			t.Errorf("key %q: code %d body %q, want %q", key, w.Code, w.Body.String(), want)
		}
	}
}
