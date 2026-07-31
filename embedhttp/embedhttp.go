package embedhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Config struct {
	BaseURL    string
	Model      string
	APIKey     string
	HTTPClient Doer
}

type Client struct {
	cfg      Config
	client   Doer
	endpoint string
}

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
