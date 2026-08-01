package llmhttp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/llminjector"
)

type fakeDoer struct {
	status      int
	content     string
	lastReqBody string
	lastAuth    string
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		f.lastReqBody = string(b)
	}
	f.lastAuth = req.Header.Get("Authorization")
	body, _ := json.Marshal(chatResponse{
		Choices: []struct {
			Message chatMessage `json:"message"`
		}{{Message: chatMessage{Role: "assistant", Content: f.content}}},
	})
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}, nil
}

func TestExtractorParsesModelJSON(t *testing.T) {
	doer := &fakeDoer{status: 200, content: `{"amount": 4200, "category": "travel"}`}
	extract := Extractor(Config{BaseURL: "https://x/v1", Model: "m", APIKey: "secret", HTTPClient: doer}, []string{"amount", "category"})

	out, err := extract("taxi to airport $4200")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["amount"] != 4200.0 || out["category"] != "travel" {
		t.Fatalf("unexpected extraction: %+v", out)
	}
	if doer.lastAuth != "Bearer secret" {
		t.Fatalf("API key not sent as bearer token, got %q", doer.lastAuth)
	}
	if !strings.Contains(doer.lastReqBody, "taxi to airport") {
		t.Fatalf("request body did not carry the input text: %s", doer.lastReqBody)
	}
}

func TestDecisionFieldIsStrippedEndToEnd(t *testing.T) {
	doer := &fakeDoer{status: 200, content: "```json\n{\"amount\": 9000, \"category\": \"electronics\", \"is_fraud\": true}\n```"}
	inj := llminjector.NewInjector()
	inj.RegisterSchema("finance.expense_fields",
		llminjector.Schema{"amount": llminjector.FieldFloat64, "category": llminjector.FieldString},
		Extractor(Config{BaseURL: "https://x/v1", Model: "m", HTTPClient: doer}, []string{"amount", "category"}),
	)

	result := inj.Extract(entities.ExtractionRequest{
		RawText:   "bought a laptop for $9000",
		SchemaRef: "finance.expense_fields",
		DomainID:  "finance",
	})
	if !result.Success {
		t.Fatalf("expected success, got errors: %v", result.ValidationErrors)
	}
	if _, present := result.StructuredPayload["is_fraud"]; present {
		t.Fatalf("decision field leaked through a real extraction: %+v", result.StructuredPayload)
	}
	if result.StructuredPayload["amount"] != 9000.0 {
		t.Fatalf("expected amount 9000, got %+v", result.StructuredPayload)
	}
}

func TestNon200IsReportedAsError(t *testing.T) {
	doer := &fakeDoer{status: 500, content: "boom"}
	extract := Extractor(Config{BaseURL: "https://x/v1", Model: "m", HTTPClient: doer}, []string{"amount"})
	if _, err := extract("anything"); err == nil {
		t.Fatalf("expected an error for a non-200 response")
	}
}

func TestNonJSONContentIsReportedAsError(t *testing.T) {
	doer := &fakeDoer{status: 200, content: "I think the amount is about 4200 dollars."}
	extract := Extractor(Config{BaseURL: "https://x/v1", Model: "m", HTTPClient: doer}, []string{"amount"})
	if _, err := extract("anything"); err == nil {
		t.Fatalf("expected an error when the model returns prose instead of JSON")
	}
}

func TestStripCodeFence(t *testing.T) {
	cases := map[string]string{
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"```\n{\"a\":1}\n```":     `{"a":1}`,
		`{"a":1}`:                 `{"a":1}`,
	}
	for in, want := range cases {
		if got := stripCodeFence(in); got != want {
			t.Fatalf("stripCodeFence(%q) = %q, want %q", in, got, want)
		}
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

func TestChatRejectsAnOversizedResponse(t *testing.T) {
	_, err := Chat(Config{BaseURL: "https://x/v1", Model: "m", HTTPClient: oversizedDoer{}}, "s", "u")
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("an unbounded response must be rejected, got %v", err)
	}
}
