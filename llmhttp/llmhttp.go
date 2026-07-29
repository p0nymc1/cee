// Package llmhttp is a real backend for the edge LLM injector that talks to
// any OpenAI-compatible chat-completions endpoint (OpenAI, DeepSeek, Qwen, a
// local vLLM/Ollama server, ...) using only net/http and encoding/json.
//
// It is deliberately SDK-free: speaking the HTTP API directly keeps the whole
// cee module at zero external dependencies, the invariant the rest of the
// project holds. Point Config.BaseURL at whichever endpoint you run.
//
// This is the first component that leaves the in-memory scaffold and does
// real I/O, yet it changes nothing about the injector's guarantees: whatever
// the model returns still passes through llminjector.Injector, which strips
// the payload down to the schema-declared fields. So even if a model tries to
// return a decision field like "is_fraud", it never reaches the deterministic
// engine -- the extraction-only red line holds across a real network call.
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

// Doer is the slice of *http.Client the extractor needs. Tests inject a fake
// so the suite stays hermetic and offline; production passes an *http.Client.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config points the extractor at an endpoint.
type Config struct {
	BaseURL    string // e.g. https://api.openai.com/v1 or http://localhost:11434/v1
	Model      string // e.g. gpt-4o-mini, deepseek-chat, qwen-turbo
	APIKey     string // sent as a Bearer token when non-empty
	HTTPClient Doer   // defaults to http.DefaultClient
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

// Extractor returns an llminjector.Extractor that asks the configured model
// to pull exactly the named fields out of the input text and return them as a
// JSON object. Pass the same field names the schema declares.
// Chat sends one system+user exchange and returns the model's reply with any
// surrounding code fence removed.
//
// Exported because building a workflow from a description (see the draft
// package) needs the same plumbing as extracting fields from a document: one
// call, temperature zero, JSON back. Two copies of this would drift.
func Chat(cfg Config, system, user string) (string, error) {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"

	reqBody, err := json.Marshal(chatRequest{
		Model:       cfg.Model,
		Temperature: 0, // deterministic-as-possible, not creativity
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

// Extractor returns an llminjector.Extractor that asks the configured model
// to pull exactly the named fields out of the input text and return them as a
// JSON object. Pass the same field names the schema declares.
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

// stripCodeFence removes a leading ```json / ``` fence and trailing ``` that
// chat models often wrap JSON in, so the payload parses cleanly.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		// drop an optional language tag on the first fence line (e.g. "json")
		if first := strings.TrimSpace(s[:i]); first == "" || !strings.ContainsAny(first, "{[") {
			s = s[i+1:]
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
