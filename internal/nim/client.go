// Package nim is a minimal OpenAI-compatible chat-completions client (NVIDIA NIM, Krutrim).
package nim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	URL, Key, Model string
	HTTP       *http.Client
}

func New(url, key, model string) *Client {
	return &Client{URL: url, Key: key, Model: model, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Chat returns the assistant's final content (reasoning_content is ignored).
func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"max_tokens":  4096,
		"temperature": 0,
		"stream":      false,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(body))
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
		return "", fmt.Errorf("nim status %d: %.200s", resp.StatusCode, raw)
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

// Message is one chat turn. An assistant turn may carry ToolCalls; a "tool" turn answers one by ToolCallID.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // a JSON object, as text
	} `json:"function"`
}

// Tool is a function the model may ask for; Parameters is a JSON Schema object.
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// Complete runs one non-streaming turn with tools on offer and returns the assistant message, which has either Content or ToolCalls.
func (c *Client) Complete(ctx context.Context, system string, msgs []Message, tools []Tool) (Message, error) {
	req := map[string]any{
		"model":       c.Model,
		"messages":    append([]Message{{Role: "system", Content: system}}, msgs...),
		"max_tokens":  4096,
		"temperature": 0.2,
		"stream":      false,
	}
	if len(tools) > 0 {
		req["tools"], req["tool_choice"] = tools, "auto"
	}
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.Key)
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return Message{}, fmt.Errorf("nim status %d: %.200s", resp.StatusCode, raw)
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return Message{}, fmt.Errorf("nim: bad response")
	}
	m := out.Choices[0].Message
	m.Role = "assistant"
	return m, nil
}

// ChatStream sends the conversation and calls onDelta with each piece of the answer as it arrives (reasoning_content is ignored).
// The client's 90 s timeout is not used here; ctx bounds the stream.
func (c *Client) ChatStream(ctx context.Context, system string, msgs []Message, onDelta func(string)) error {
	body, _ := json.Marshal(map[string]any{
		"model":       c.Model,
		"messages":    append([]Message{{Role: "system", Content: system}}, msgs...),
		"max_tokens":  4096,
		"temperature": 0.3,
		"stream":      true,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("nim status %d: %s", resp.StatusCode, raw)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &ev) == nil && len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			onDelta(ev.Choices[0].Delta.Content)
		}
	}
	return sc.Err()
}
