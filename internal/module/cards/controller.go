package cards

import (
	"net/http"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"ledger-backend/internal/httpx"
)

type Controller struct{ svc *Service }

func NewController(svc *Service) *Controller { return &Controller{svc} }

func (c *Controller) Create(w http.ResponseWriter, r *http.Request) {
	var req Card
	if !httpx.Decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpx.Err(w, 400, "name required")
		return
	}
	if l := req.Last4; l != "" && (len(l) != 4 || strings.Trim(l, "0123456789") != "") {
		httpx.Err(w, 400, "last4 must be 4 digits")
		return
	}
	card, err := c.svc.Create(r.Context(), req)
	switch {
	case mongo.IsDuplicateKeyError(err):
		httpx.Err(w, 409, "card exists")
	case err != nil:
		httpx.Err(w, 500, "could not create card")
	default:
		httpx.WriteJSON(w, 201, card)
	}
}

func (c *Controller) List(w http.ResponseWriter, r *http.Request) {
	cards, err := c.svc.List(r.Context())
	if err != nil {
		httpx.Err(w, 500, "could not list cards")
		return
	}
	httpx.WriteJSON(w, 200, cards)
}
