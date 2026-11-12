package swiggy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MCP servers, as paths under Base.
const (
	Food      = "/food"
	Instamart = "/im"
)

// ponytail: protocolVersion is not in Swiggy's docs; this is the standard MCP Streamable HTTP version. Bump if the server rejects it.
const protocolVersion = "2025-06-18"

// Client calls Swiggy MCP tools for one user. It keeps one session per server, as Swiggy asks, and initialises them one at a time.
type Client struct {
	auth     *Auth
	base     string
	hc       *http.Client
	mu       sync.Mutex // serialises calls; Swiggy wants one session per user, not parallel ones
	sessions map[string]string
	id       atomic.Int64
}

func NewClient(auth *Auth) *Client {
	return &Client{auth: auth, base: auth.base, hc: &http.Client{Timeout: 45 * time.Second}, sessions: map[string]string{}}
}

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// httpErr carries the status so Call can tell a dead session (404) or token (401) from the rest.
type httpErr struct {
	status int
	body   string
}

func (e *httpErr) Error() string { return fmt.Sprintf("swiggy answered %d: %s", e.status, e.body) }

// post sends one JSON-RPC message. Notifications (id 0) expect no body. Replies come as JSON or as an SSE stream.
func (c *Client) post(ctx context.Context, server, token, session string, id int64, method string, params any) (rpcReply, string, error) {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	if id != 0 {
		msg["id"] = id
	}
	buf, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+server, bytes.NewReader(buf))
	if err != nil {
		return rpcReply{}, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", protocolVersion)
	}
	res, err := c.hc.Do(req)
	if err != nil {
		return rpcReply{}, "", err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return rpcReply{}, "", &httpErr{res.StatusCode, strings.TrimSpace(string(b))}
	}
	sid := res.Header.Get("Mcp-Session-Id")
	if id == 0 {
		return rpcReply{}, sid, nil
	}
	r, err := readReply(res.Body, strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream"), id)
	return r, sid, err
}

// readReply finds the reply to request id, in a plain JSON body or in the data lines of an SSE stream.
func readReply(body io.Reader, sse bool, id int64) (rpcReply, error) {
	var r rpcReply
	if !sse {
		return r, json.NewDecoder(io.LimitReader(body, 8<<20)).Decode(&r)
	}
	want := fmt.Sprint(id)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	var data strings.Builder
	flush := func() bool {
		defer data.Reset()
		var got rpcReply
		if data.Len() == 0 || json.Unmarshal([]byte(data.String()), &got) != nil || string(got.ID) != want {
			return false // a progress notification or a reply to something else
		}
		r = got
		return true
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" { // end of one event
			if flush() {
				return r, nil
			}
			continue
		}
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			data.WriteString(strings.TrimPrefix(d, " "))
		}
	}
	if flush() {
		return r, nil
	}
	if err := sc.Err(); err != nil {
		return r, err
	}
	return r, errors.New("swiggy closed the stream without a reply")
}

func (c *Client) session(ctx context.Context, server, token string) (string, error) {
	if s, ok := c.sessions[server]; ok {
		return s, nil
	}
	r, sid, err := c.post(ctx, server, token, "", c.id.Add(1), "initialize", map[string]any{
		"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "ledger-pantry", "version": "1.0"},
	})
	if err == nil && r.Error != nil {
		err = fmt.Errorf("swiggy initialize: %s", r.Error.Message)
	}
	if err != nil {
		return "", err
	}
	if _, _, err := c.post(ctx, server, token, sid, 0, "notifications/initialized", nil); err != nil {
		return "", err
	}
	c.sessions[server] = sid
	return sid, nil
}

// Call runs one tool and decodes structuredContent.data into out. A dead session is re-opened and a rejected token refreshed, once each.
func (c *Client) Call(ctx context.Context, server, tool string, args map[string]any, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	force := false
	for attempt := 0; ; attempt++ {
		token, err := c.auth.Token(ctx, force)
		if err != nil {
			return err
		}
		sid, err := c.session(ctx, server, token)
		var r rpcReply
		if err == nil {
			r, _, err = c.post(ctx, server, token, sid, c.id.Add(1), "tools/call", map[string]any{"name": tool, "arguments": args})
		}
		var he *httpErr
		retry := attempt == 0 && (errors.As(err, &he) && (he.status == 401 || he.status == 404) || err == nil && r.Error != nil && r.Error.Code == -32001)
		if retry {
			delete(c.sessions, server)
			force = he == nil || he.status == 401
			continue
		}
		if err != nil {
			return err
		}
		if r.Error != nil {
			if r.Error.Code == -32001 {
				return ErrNotConnected
			}
			return fmt.Errorf("swiggy %s: %s", tool, r.Error.Message)
		}
		return decodeResult(tool, r.Result, out)
	}
}

// decodeResult reads the data out of a tool result. Swiggy puts it in structuredContent, as the data itself (what the live server does:
// {"orders":[...]}) or, per its docs, wrapped as {success, data, error}. content is prose and is only used to explain a failure.
func decodeResult(tool string, raw json.RawMessage, out any) error {
	var res struct {
		IsError    bool            `json:"isError"`
		Structured json.RawMessage `json:"structuredContent"`
		Content    []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("swiggy %s: unreadable result: %w", tool, err)
	}
	var env struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
		Message string          `json:"message"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(res.Structured, &env)
	if res.IsError || env.Success != nil && !*env.Success {
		msg := env.Message
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		if msg == "" && len(res.Content) > 0 {
			msg = res.Content[0].Text
		}
		return fmt.Errorf("swiggy %s: %s", tool, msg[:min(len(msg), 300)])
	}
	if len(res.Structured) == 0 {
		return fmt.Errorf("swiggy %s: no structuredContent in result", tool)
	}
	if env.Success != nil && env.Data != nil {
		return json.Unmarshal(env.Data, out)
	}
	return json.Unmarshal(res.Structured, out)
}
