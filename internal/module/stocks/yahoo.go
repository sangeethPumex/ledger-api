package stocks

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Yahoo prices symbols from the unofficial chart endpoint (no key). It has no batch call, so symbols go out concurrently.
type Yahoo struct {
	BaseURL string // e.g. https://query1.finance.yahoo.com/v8/finance/chart
	http    *http.Client
}

func NewYahoo(baseURL string) *Yahoo {
	return &Yahoo{BaseURL: baseURL, http: &http.Client{Timeout: 8 * time.Second}}
}

func (y *Yahoo) one(ctx context.Context, symbol string) (Quote, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", y.BaseURL+"/"+url.PathEscape(symbol)+"?range=1d&interval=1d", nil)
	if err != nil {
		return Quote{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0") // Yahoo answers 429 to the default Go agent
	resp, err := y.http.Do(req)
	if err != nil {
		return Quote{}, err
	}
	defer resp.Body.Close()
	var j struct {
		Chart struct {
			Result []struct {
				Meta struct {
					Price     float64 `json:"regularMarketPrice"`
					PrevClose float64 `json:"chartPreviousClose"`
					LongName  string  `json:"longName"`
					ShortName string  `json:"shortName"`
				} `json:"meta"`
			} `json:"result"`
		} `json:"chart"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil || resp.StatusCode != 200 || len(j.Chart.Result) == 0 || !(j.Chart.Result[0].Meta.Price > 0) {
		return Quote{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	m := j.Chart.Result[0].Meta
	name := m.LongName
	if name == "" {
		name = m.ShortName
	}
	return Quote{Symbol: symbol, Name: name, Price: m.Price, PrevClose: m.PrevClose, FetchedAt: time.Now()}, nil
}

func (y *Yahoo) Quotes(ctx context.Context, symbols []string) (map[string]Quote, error) {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = map[string]Quote{}
		sem = make(chan struct{}, 5) // be polite: Yahoo rate-limits bursts
	)
	for _, s := range symbols {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			q, err := y.one(ctx, s)
			if err != nil {
				log.Printf("yahoo %s: %v", s, err)
				return
			}
			mu.Lock()
			out[s] = q
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(out) == 0 && len(symbols) > 0 {
		return out, fmt.Errorf("no quotes from yahoo")
	}
	return out, nil
}
