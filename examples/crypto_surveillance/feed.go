package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Quote struct {
	Asset       string
	USD         float64
	ChangePct24 float64
	Volume24    float64
	QuotedAt    time.Time
}

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type Feed struct {
	Endpoint string
	Client   Doer
}

const defaultEndpoint = "https://api.coingecko.com/api/v3/simple/price"

func NewFeed() *Feed {
	return &Feed{
		Endpoint: defaultEndpoint,

		Client: &http.Client{Timeout: 20 * time.Second},
	}
}

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
