package intentrouter

import (
	"errors"
	"testing"

	"github.com/cee-project/cee/entities"
)

// fakeVectorizer maps known phrases to hand-built vectors in a tiny concept
// space [login, expense], so we can prove semantic matching without a network
// and without depending on a real embedding model's exact numbers.
type fakeVectorizer struct {
	vectors map[string][]float64
	err     error
}

func (f *fakeVectorizer) Vectorize(text string) ([]float64, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.vectors[text]; ok {
		return v, nil
	}
	return []float64{0, 0}, nil // unknown -> orthogonal to everything
}

func semanticRouter(v Vectorizer) *Router {
	r := NewRouter(0.8)
	r.SetVectorizer(v)
	r.RegisterNode(entities.IntentNode{
		NodeID:           "security.suspicious_login",
		DomainID:         "security",
		Examples:         []string{"suspicious login"},
		EntryWorkflowRef: "security.contain",
	})
	r.RegisterNode(entities.IntentNode{
		NodeID:           "finance.duplicate_expense",
		DomainID:         "security", // same domain, different concept
		Examples:         []string{"duplicate expense report"},
		EntryWorkflowRef: "finance.flag",
	})
	return r
}

// The point of embeddings: a query with ZERO token overlap still matches the
// right intent by meaning. "unusual sign-in from a new device" shares no word
// with "suspicious login", so lexical scoring would miss it.
func TestSemanticMatchAcrossVocabulary(t *testing.T) {
	v := &fakeVectorizer{vectors: map[string][]float64{
		"suspicious login":                  {1, 0},
		"duplicate expense report":          {0, 1},
		"unusual sign-in from a new device": {0.96, 0.1},
	}}
	r := semanticRouter(v)

	result := r.Match("security", "unusual sign-in from a new device")
	if !result.Matched {
		t.Fatalf("expected a semantic match, got %+v", result)
	}
	if result.NodeRef != "security.suspicious_login" {
		t.Fatalf("expected the login intent, got %s", result.NodeRef)
	}

	// Sanity: the same query has no lexical overlap, so the lexical router
	// would not clear the threshold.
	lex := NewRouter(0.8)
	lex.RegisterNode(entities.IntentNode{NodeID: "security.suspicious_login", DomainID: "security", Examples: []string{"suspicious login"}, EntryWorkflowRef: "security.contain"})
	if lex.Match("security", "unusual sign-in from a new device").Matched {
		t.Fatalf("lexical router should NOT have matched a zero-overlap query")
	}
}

// If the embeddings endpoint fails, Match must not error (it has no error to
// return) -- it degrades to lexical scoring instead.
func TestSemanticFailureDegradesToLexical(t *testing.T) {
	r := semanticRouter(&fakeVectorizer{err: errors.New("endpoint down")})

	// This query is a full lexical match for "duplicate expense report", so
	// the lexical fallback clears the 0.8 threshold even though embeddings
	// are unavailable.
	result := r.Match("security", "duplicate expense report")
	if !result.Matched || result.NodeRef != "finance.duplicate_expense" {
		t.Fatalf("expected lexical fallback to match the expense intent, got %+v", result)
	}
}

func TestCosine(t *testing.T) {
	if got := cosine([]float64{1, 0}, []float64{1, 0}); got < 0.999 {
		t.Fatalf("identical vectors should score ~1, got %v", got)
	}
	if got := cosine([]float64{1, 0}, []float64{0, 1}); got != 0 {
		t.Fatalf("orthogonal vectors should score 0, got %v", got)
	}
}
