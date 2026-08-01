package embedhttp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/intentrouter"
)

var _ intentrouter.Vectorizer = (*Client)(nil)

type fakeDoer struct {
	status      int
	embedding   []float64
	lastReqBody string
	lastAuth    string
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		f.lastReqBody = string(b)
	}
	f.lastAuth = req.Header.Get("Authorization")
	body, _ := json.Marshal(embedResponse{
		Data: []struct {
			Embedding []float64 `json:"embedding"`
		}{{Embedding: f.embedding}},
	})
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}, nil
}

func TestVectorizeParsesEmbedding(t *testing.T) {
	doer := &fakeDoer{status: 200, embedding: []float64{0.1, 0.2, 0.3}}
	c := New(Config{BaseURL: "https://x/v1", Model: "text-embedding-3-small", APIKey: "secret", HTTPClient: doer})

	vec, err := c.Vectorize("suspicious login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Fatalf("unexpected embedding: %v", vec)
	}
	if doer.lastAuth != "Bearer secret" {
		t.Fatalf("API key not sent as bearer token, got %q", doer.lastAuth)
	}
	if !strings.Contains(doer.lastReqBody, "suspicious login") {
		t.Fatalf("request did not carry the input text: %s", doer.lastReqBody)
	}
}

func TestVectorizeNon200IsError(t *testing.T) {
	c := New(Config{BaseURL: "https://x/v1", Model: "m", HTTPClient: &fakeDoer{status: 429}})
	if _, err := c.Vectorize("x"); err == nil {
		t.Fatalf("expected an error for a non-200 response")
	}
}

func TestVectorizeEmptyEmbeddingIsError(t *testing.T) {
	c := New(Config{BaseURL: "https://x/v1", Model: "m", HTTPClient: &fakeDoer{status: 200, embedding: nil}})
	if _, err := c.Vectorize("x"); err == nil {
		t.Fatalf("expected an error when the response has no embedding")
	}
}

type endlessBody struct{}

func (endlessBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

type oversizedDoer struct{}

func (oversizedDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(endlessBody{}), Header: make(http.Header)}, nil
}

func TestVectorizeRejectsAnOversizedResponse(t *testing.T) {
	c := New(Config{BaseURL: "https://x/v1", Model: "m", HTTPClient: oversizedDoer{}})
	_, err := c.Vectorize("hello")
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("an unbounded response must be rejected, got %v", err)
	}
}
