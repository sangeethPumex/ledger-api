package swiggy

import (
	"log"
	"net/http"

	"ledger-backend/internal/httpx"
)

type Controller struct{ auth *Auth }

func NewController(auth *Auth) *Controller { return &Controller{auth} }

func (c *Controller) Status(w http.ResponseWriter, r *http.Request) {
	ok, since, err := c.auth.Status(r.Context())
	if err != nil {
		httpx.Err(w, 500, "could not read the swiggy link")
		return
	}
	out := map[string]any{"connected": ok}
	if ok && !since.IsZero() {
		out["connected_at"] = since
	}
	httpx.WriteJSON(w, 200, out)
}

// Connect returns the Swiggy sign-in page; the browser goes there and comes back to the redirect URI with ?code&state.
func (c *Controller) Connect(w http.ResponseWriter, r *http.Request) {
	u, err := c.auth.ConnectURL(r.Context())
	if err != nil {
		log.Printf("swiggy connect: %v", err)
		httpx.Err(w, 502, "could not start the Swiggy sign-in")
		return
	}
	httpx.WriteJSON(w, 200, map[string]string{"url": u})
}

func (c *Controller) Callback(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code, State string }
	if !httpx.Decode(w, r, &req) {
		return
	}
	if req.Code == "" || req.State == "" || len(req.Code) > 4096 || len(req.State) > 256 {
		httpx.Err(w, 400, "code and state are required")
		return
	}
	if err := c.auth.Callback(r.Context(), req.Code, req.State); err != nil {
		log.Printf("swiggy callback: %v", err)
		httpx.Err(w, 400, "Swiggy sign-in did not finish; try connecting again")
		return
	}
	w.WriteHeader(204)
}

func (c *Controller) Disconnect(w http.ResponseWriter, r *http.Request) {
	if err := c.auth.Disconnect(r.Context()); err != nil {
		httpx.Err(w, 500, "could not disconnect")
		return
	}
	w.WriteHeader(204)
}

func RegisterRoutes(mux *http.ServeMux, c *Controller, auth func(http.Handler) http.Handler) {
	mux.Handle("GET /grocery/swiggy", auth(http.HandlerFunc(c.Status)))
	mux.Handle("POST /grocery/swiggy/connect", auth(http.HandlerFunc(c.Connect)))
	mux.Handle("POST /grocery/swiggy/callback", auth(http.HandlerFunc(c.Callback)))
	mux.Handle("DELETE /grocery/swiggy", auth(http.HandlerFunc(c.Disconnect)))
}
