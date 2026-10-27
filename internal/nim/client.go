// Package nim is a minimal NVIDIA NIM chat-completions client.
package nim

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	Key, Model string
	HTTP       *http.Client
}

func New(key, model string) *Client {
	return &Client{Key: key, Model: model, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Chat returns the assistant's final content (reasoning_content is ignored).
func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"max_tokens":       4096,
		"reasoning_budget": 2048,
		"temperature":      0,
		"stream":           false,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://integrate.api.nvidia.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("nim status %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("nim: bad response")
	}
	return out.Choices[0].Message.Content, nil
}
