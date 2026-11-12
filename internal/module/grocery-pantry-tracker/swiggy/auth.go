// Package swiggy connects one user to Swiggy MCP (Food and Instamart): OAuth 2.1 with PKCE, then JSON-RPC tool calls.
// Docs: https://mcp.swiggy.com/builders/docs/start/authenticate
package swiggy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	Base       = "https://mcp.swiggy.com"
	Collection = "grocery_swiggy_auth"
	docID      = "swiggy"
)

// ErrNotConnected means there is no usable token: the user has to connect (again).
var ErrNotConnected = errors.New("swiggy is not connected")

// link is the one stored document: the registered client, the tokens, and a sign-in in progress.
type link struct {
	ClientID       string    `bson:"client_id,omitempty"`
	ClientRedirect string    `bson:"client_redirect,omitempty"` // the redirect the client was registered with
	Access         string    `bson:"access,omitempty"`
	Refresh        string    `bson:"refresh,omitempty"`
	ExpiresAt      time.Time `bson:"expires_at,omitempty"`
	ConnectedAt    time.Time `bson:"connected_at,omitempty"`
	State          string    `bson:"state,omitempty"`
	Verifier       string    `bson:"verifier,omitempty"`
	StartedAt      time.Time `bson:"started_at,omitempty"`
}

type Auth struct {
	col      *mongo.Collection
	redirect string
	base     string
	hc       *http.Client
	mu       sync.Mutex // one refresh at a time: refresh tokens rotate
}

func NewAuth(db *mongo.Database, redirect string) *Auth {
	return &Auth{col: db.Collection(Collection), redirect: redirect, base: Base, hc: &http.Client{Timeout: 20 * time.Second}}
}

func (a *Auth) load(ctx context.Context) (link, error) {
	var l link
	err := a.col.FindOne(ctx, bson.M{"_id": docID}).Decode(&l)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = nil
	}
	return l, err
}

func (a *Auth) set(ctx context.Context, set, unset bson.M) error {
	upd := bson.M{}
	if len(set) > 0 {
		upd["$set"] = set
	}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	_, err := a.col.UpdateOne(ctx, bson.M{"_id": docID}, upd, options.UpdateOne().SetUpsert(true))
	return err
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// postJSON sends body as JSON (Swiggy's token endpoint takes JSON, not a form) and decodes the reply into out.
func (a *Auth) postJSON(ctx context.Context, path string, body, out any) (int, error) {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", a.base+path, bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := a.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	return res.StatusCode, json.NewDecoder(res.Body).Decode(out)
}

// ConnectURL registers this app with Swiggy when needed (RFC 7591, public client) and returns the page where the user signs in with
// phone and OTP. The PKCE verifier and state wait in the database for the callback.
func (a *Auth) ConnectURL(ctx context.Context) (string, error) {
	l, err := a.load(ctx)
	if err != nil {
		return "", err
	}
	if l.ClientID == "" || l.ClientRedirect != a.redirect {
		var reg struct {
			ClientID string `json:"client_id"`
			Error    string `json:"error_description"`
		}
		code, err := a.postJSON(ctx, "/auth/register", map[string]any{
			"client_name": "Ledger Pantry Tracker", "redirect_uris": []string{a.redirect},
			"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "token_endpoint_auth_method": "none",
		}, &reg)
		if err != nil || reg.ClientID == "" {
			return "", fmt.Errorf("swiggy client registration failed (%d): %v %s", code, err, reg.Error)
		}
		l.ClientID = reg.ClientID
	}
	verifier, state := random(32), random(16)
	sum := sha256.Sum256([]byte(verifier))
	if err := a.set(ctx, bson.M{"client_id": l.ClientID, "client_redirect": a.redirect, "state": state, "verifier": verifier, "started_at": time.Now()}, nil); err != nil {
		return "", err
	}
	q := url.Values{
		"response_type": {"code"}, "client_id": {l.ClientID}, "redirect_uri": {a.redirect}, "scope": {"mcp:tools"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
	}
	return a.base + "/auth/authorize?" + q.Encode(), nil
}

type tokenReply struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int64  `json:"expires_in"`
	Error     string `json:"error"`
	Desc      string `json:"error_description"`
}

func (a *Auth) save(ctx context.Context, t tokenReply, extra bson.M) error {
	set := bson.M{"access": t.Access, "expires_at": time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)}
	if t.Refresh != "" {
		set["refresh"] = t.Refresh // rotates: always keep the newest
	}
	for k, v := range extra {
		set[k] = v
	}
	return a.set(ctx, set, nil)
}

// Callback finishes the sign-in: the state must match the one ConnectURL stored (and be under 10 minutes old), then the code is swapped for tokens.
func (a *Auth) Callback(ctx context.Context, code, state string) error {
	l, err := a.load(ctx)
	if err != nil {
		return err
	}
	if l.State == "" || subtle.ConstantTimeCompare([]byte(state), []byte(l.State)) != 1 || time.Since(l.StartedAt) > 10*time.Minute {
		return errors.New("this sign-in link is stale; start again")
	}
	var t tokenReply
	status, err := a.postJSON(ctx, "/auth/token", map[string]string{
		"grant_type": "authorization_code", "code": code, "code_verifier": l.Verifier, "redirect_uri": l.ClientRedirect, "client_id": l.ClientID,
	}, &t)
	if err != nil || t.Access == "" {
		return fmt.Errorf("swiggy token exchange failed (%d): %v %s %s", status, err, t.Error, t.Desc)
	}
	if err := a.save(ctx, t, bson.M{"connected_at": time.Now()}); err != nil {
		return err
	}
	return a.set(ctx, nil, bson.M{"state": "", "verifier": "", "started_at": ""})
}

// Token returns a live access token, refreshing it when under a minute is left (or when force is set, after a 401).
func (a *Auth) Token(ctx context.Context, force bool) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, err := a.load(ctx)
	if err != nil {
		return "", err
	}
	if l.Access == "" {
		return "", ErrNotConnected
	}
	if !force && time.Until(l.ExpiresAt) > time.Minute {
		return l.Access, nil
	}
	if l.Refresh == "" {
		return "", ErrNotConnected
	}
	var t tokenReply
	status, err := a.postJSON(ctx, "/auth/token", map[string]string{"grant_type": "refresh_token", "refresh_token": l.Refresh, "client_id": l.ClientID}, &t)
	if status == http.StatusBadRequest { // invalid_grant / invalid_request: retrying will not help, the user signs in again
		_ = a.set(ctx, nil, bson.M{"access": "", "refresh": "", "expires_at": ""})
		return "", ErrNotConnected
	}
	if err != nil || t.Access == "" {
		return "", fmt.Errorf("swiggy token refresh failed (%d): %v %s", status, err, t.Desc)
	}
	return t.Access, a.save(ctx, t, nil)
}

// Status says whether a token is stored, and since when.
func (a *Auth) Status(ctx context.Context) (bool, time.Time, error) {
	l, err := a.load(ctx)
	return l.Access != "", l.ConnectedAt, err
}

// Disconnect revokes the Swiggy session (best effort) and forgets the tokens; the registered client stays for the next connect.
func (a *Auth) Disconnect(ctx context.Context) error {
	if l, err := a.load(ctx); err == nil && l.Access != "" {
		if req, err := http.NewRequestWithContext(ctx, "POST", a.base+"/auth/logout", nil); err == nil {
			req.Header.Set("Authorization", "Bearer "+l.Access)
			if res, err := a.hc.Do(req); err == nil {
				res.Body.Close()
			}
		}
	}
	return a.set(ctx, nil, bson.M{"access": "", "refresh": "", "expires_at": "", "connected_at": ""})
}
