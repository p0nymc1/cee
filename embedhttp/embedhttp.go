// Package embedhttp is a real backend for the intent router's Vectorizer:
// it turns text into an embedding by calling any OpenAI-compatible
// /v1/embeddings endpoint (OpenAI, a local text-embedding server, ...) using
// only net/http and encoding/json.
//
// Like llmhttp, it is SDK-free on purpose, so wiring real semantic intent
// matching into CEE costs zero external dependencies. A *Client satisfies
// intentrouter.Vectorizer, so:
//
//	router.SetVectorizer(embedhttp.New(embedhttp.Config{BaseURL: ..., Model: ...}))
//
// upgrades a router from token-overlap to cosine-over-embeddings without any
// other change.
package embedhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Doer is the slice of *http.Client the client needs; tests inject a fake so
// the suite stays hermetic and offline.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config points the client at an embeddings endpoint.
type Config struct {
	BaseURL    string // e.g. https://api.openai.com/v1
	Model      string // e.g. text-embedding-3-small
	APIKey     string // sent as a Bearer token when non-empty
	HTTPClient Doer   // defaults to http.DefaultClient
}

// Client is an intentrouter.Vectorizer backed by an HTTP embeddings endpoint.
type Client struct {
	cfg      Config
	client   Doer
	endpoint string
}

// New builds a Client from cfg.
func New(cfg Config) *Client {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Client{
		cfg:      cfg,
		client:   client,
		endpoint: strings.TrimRight(cfg.BaseURL, "/") + "/embeddings",
	}
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
}

// Vectorize satisfies intentrouter.Vectorizer.
func (c *Client) Vectorize(text string) ([]float64, error) {
	reqBody, err := json.Marshal(embedRequest{Model: c.cfg.Model, Input: text})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("embedhttp: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embedhttp: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedhttp: endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed embedResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("embedhttp: cannot decode response: %w", err)
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedhttp: response contained no embedding")
	}
	return parsed.Data[0].Embedding, nil
}
