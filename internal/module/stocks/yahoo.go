package stocks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Yahoo prices symbols from the unofficial chart endpoint (no key). It has no batch call, so symbols go out concurrently.
type Yahoo struct {
	BaseURL string // e.g. https://query1.finance.yahoo.com/v8/finance/chart
	http    *http.Client

	crumbMu sync.Mutex
	crumb   string // quoteSummary needs a cookie + crumb pair; fetched lazily and kept
}

func NewYahoo(baseURL string) *Yahoo {
	jar, _ := cookiejar.New(nil)
	return &Yahoo{BaseURL: baseURL, http: &http.Client{Timeout: 8 * time.Second, Jar: jar}}
}

func (y *Yahoo) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	return y.http.Do(req)
}

func (y *Yahoo) getCrumb(ctx context.Context) (string, error) {
	y.crumbMu.Lock()
	defer y.crumbMu.Unlock()
	if y.crumb != "" {
		return y.crumb, nil
	}
	if r, err := y.get(ctx, "https://fc.yahoo.com"); err == nil { // sets the session cookie; the 404 body is irrelevant
		r.Body.Close()
	}
	r, err := y.get(ctx, "https://query1.finance.yahoo.com/v1/test/getcrumb")
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 256))
	if c := strings.TrimSpace(string(b)); r.StatusCode == 200 && c != "" && !strings.Contains(c, "<") {
		y.crumb = c
	}
	if y.crumb == "" {
		return "", fmt.Errorf("no crumb (status %d)", r.StatusCode)
	}
	return y.crumb, nil
}

// domain returns the company website's host, e.g. "ril.com". answered is false when the lookup failed and should be retried later;
// an empty host with answered true means Yahoo has no website (ETFs, SME listings).
func (y *Yahoo) domain(ctx context.Context, symbol string) (host string, answered bool) {
	crumb, err := y.getCrumb(ctx)
	if err != nil {
		log.Printf("yahoo crumb: %v", err)
		return "", false
	}
	resp, err := y.get(ctx, "https://query1.finance.yahoo.com/v10/finance/quoteSummary/"+url.PathEscape(symbol)+"?modules=assetProfile&crumb="+url.QueryEscape(crumb))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 { // crumb expired
		y.crumbMu.Lock()
		y.crumb = ""
		y.crumbMu.Unlock()
		return "", false
	}
	if resp.StatusCode != 200 && resp.StatusCode != 404 {
		return "", false
	}
	var j struct {
		QuoteSummary struct {
			Result []struct {
				AssetProfile struct {
					Website string `json:"website"`
				} `json:"assetProfile"`
			} `json:"result"`
		} `json:"quoteSummary"`
	}
	if json.NewDecoder(resp.Body).Decode(&j) != nil || len(j.QuoteSummary.Result) == 0 {
		return "", resp.StatusCode == 404
	}
	u, err := url.Parse(j.QuoteSummary.Result[0].AssetProfile.Website)
	if err != nil {
		return "", true
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), true
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
			q.Domain, q.DomainTried = y.domain(ctx, s)
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
