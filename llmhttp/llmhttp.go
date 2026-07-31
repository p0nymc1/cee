package llmhttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/p0nymc1/cee/llminjector"
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

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

func Chat(cfg Config, system, user string) (string, error) {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"

	reqBody, err := json.Marshal(chatRequest{
		Model:       cfg.Model,
		Temperature: 0,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("llmhttp: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("llmhttp: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llmhttp: endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("llmhttp: cannot decode response envelope: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llmhttp: response contained no choices")
	}
	return stripCodeFence(strings.TrimSpace(parsed.Choices[0].Message.Content)), nil
}

func Extractor(cfg Config, fields []string) llminjector.Extractor {
	system := buildSystemPrompt(fields)
	return func(rawText string) (map[string]any, error) {
		content, err := Chat(cfg, system, rawText)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(content), &out); err != nil {
			return nil, fmt.Errorf("llmhttp: model did not return a JSON object: %w", err)
		}
		return out, nil
	}
}

func buildSystemPrompt(fields []string) string {
	return fmt.Sprintf(
		"You extract structured data. Read the user's message and return ONLY a JSON "+
			"object containing exactly these keys: %s. Every value must be taken from the "+
			"input text. Do not add any other keys, explanations, or commentary. If a value "+
			"is not present, use null.",
		strings.Join(fields, ", "),
	)
}

func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {

		if first := strings.TrimSpace(s[:i]); first == "" || !strings.ContainsAny(first, "{[") {
			s = s[i+1:]
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
