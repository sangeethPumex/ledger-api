package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Tool runs one of the AI's deterministic lookups. The model only explains the result: the numbers always come from here.
func (s *Service) Tool(ctx context.Context, name string, amountRupees float64, date string, days int, now time.Time) (any, error) {
	st, err := s.State(ctx, now)
	if err != nil {
		return nil, err
	}
	in := CheckInput{AmountPaise: int64(amountRupees*100 + 0.5), Date: date}
	switch name {
	case "safe_to_spend":
		return rupeeView(st.SafeNow())
	case "upcoming_money":
		if days < 1 || days > 90 {
			days = 7
		}
		return rupeeView(st.WeekAhead(days))
	case "purchase_check", "delay_scenarios":
		if msg := in.Validate(); msg != "" {
			return nil, errors.New(msg)
		}
		c := st.Check(in)
		if name == "delay_scenarios" {
			return rupeeView(map[string]any{"amountPaise": c.AmountPaise, "scenarios": c.Scenarios})
		}
		return rupeeView(c)
	}
	return nil, errors.New("unknown tool")
}

// rupeeView turns the API's paise fields into rupees, renaming them, because everything the model reads is in rupees.
func rupeeView(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return convertPaise(out), nil
}

func convertPaise(v any) any {
	switch t := v.(type) {
	case []any:
		for i := range t {
			t[i] = convertPaise(t[i])
		}
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			if n, ok := x.(float64); ok && strings.HasSuffix(k, "Paise") {
				out[strings.TrimSuffix(k, "Paise")+"Rupees"] = n / 100
			} else if k == "paise" && ok {
				out["rupees"] = n / 100
			} else {
				out[k] = convertPaise(x)
			}
		}
		return out
	}
	return v
}
