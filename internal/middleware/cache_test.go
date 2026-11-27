package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	calls, status := 0, 200
	var during func()
	c := NewCache(time.Minute, "/dashboard")
	h := APIKey("secret")(c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if during != nil {
			during()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(strconv.Itoa(calls)))
	})))
	do := func(method, url, key string) (int, string) {
		req := httptest.NewRequest(method, url, nil)
		req.Header.Set("X-API-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	want := func(step, method, url, body string) {
		t.Helper()
		if _, got := do(method, url, "secret"); got != body {
			t.Errorf("%s: got %q want %q", step, got, body)
		}
	}
	want("first read", "GET", "/dashboard", "1")
	want("kept", "GET", "/dashboard", "1")
	want("other query", "GET", "/dashboard?period=last", "2")
	if code, body := do("GET", "/dashboard", "wrong"); code != 401 || body == "1" {
		t.Errorf("a wrong key read the cache: %d %q", code, body)
	}
	want("search post is a read", "POST", "/transactions/search", "3")
	want("still kept", "GET", "/dashboard", "1")
	want("write", "PATCH", "/transactions/1", "4")
	want("cleared by the write", "GET", "/dashboard", "5")
	want("unlisted path", "GET", "/accounts", "6")
	want("unlisted path again", "GET", "/accounts", "7")

	c.Clear()
	status = 500
	want("error", "GET", "/dashboard", "8")
	status = 200
	want("error not kept", "GET", "/dashboard", "9")

	c.Clear()
	during = c.Clear // a transaction lands while the reply is being built
	want("raced", "GET", "/dashboard", "10")
	during = nil
	want("raced reply not kept", "GET", "/dashboard", "11")

	c.ttl = 0
	want("expired", "GET", "/dashboard", "12")
}
