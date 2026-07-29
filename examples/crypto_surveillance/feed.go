package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Quote is one asset as the market data provider reported it.
type Quote struct {
	Asset       string
	USD         float64
	ChangePct24 float64
	Volume24    float64
	QuotedAt    time.Time
}

// Doer is the slice of http.Client the feed needs, so tests can supply canned
// responses instead of reaching the network. Same arrangement llmhttp and
// embedhttp use.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Feed reads spot quotes from a CoinGecko-compatible endpoint. Standard
// library only -- no SDK, so it stays inside the core module's zero-dependency
// rule.
type Feed struct {
	Endpoint string
	Client   Doer
}

const defaultEndpoint = "https://api.coingecko.com/api/v3/simple/price"

func NewFeed() *Feed {
	return &Feed{
		Endpoint: defaultEndpoint,
		// A surveillance sweep that hangs is worse than one that reports it
		// could not read the market.
		Client: &http.Client{Timeout: 20 * time.Second},
	}
}

// Quotes fetches the named assets. A failure here is reported, never faked:
// inventing a price would be far worse than saying the market could not be
// read.
func (f *Feed) Quotes(assets []string) ([]Quote, error) {
	q := url.Values{}
	q.Set("ids", strings.Join(assets, ","))
	q.Set("vs_currencies", "usd")
	q.Set("include_24hr_change", "true")
	q.Set("include_24hr_vol", "true")
	q.Set("include_last_updated_at", "true")

	req, err := http.NewRequest(http.MethodGet, f.Endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("building the quote request: %w", err)
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reading the market: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("market data provider returned %s", resp.Status)
	}

	var raw map[string]struct {
		USD         float64 `json:"usd"`
		ChangePct24 float64 `json:"usd_24h_change"`
		Volume24    float64 `json:"usd_24h_vol"`
		UpdatedAt   int64   `json:"last_updated_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding the quote response: %w", err)
	}

	// Iterate the requested order rather than the map, so a sweep reports the
	// same assets in the same order every run.
	quotes := make([]Quote, 0, len(assets))
	for _, asset := range assets {
		v, ok := raw[asset]
		if !ok {
			continue
		}
		quotes = append(quotes, Quote{
			Asset:       asset,
			USD:         v.USD,
			ChangePct24: v.ChangePct24,
			Volume24:    v.Volume24,
			QuotedAt:    time.Unix(v.UpdatedAt, 0).UTC(),
		})
	}
	return quotes, nil
}
