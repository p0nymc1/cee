// Package intentrouter implements CEE's intent routing layer. Out of the box
// it matches free text against each domain's registered IntentNodes using
// token-overlap similarity -- lightweight and dependency-free. Attach a
// Vectorizer with SetVectorizer to upgrade to real semantic matching (e.g.
// embeddings from embedhttp) without changing the Router API: RegisterNode
// and Match keep the same shapes, only the scoring behind them changes.
package intentrouter

import (
	"math"
	"regexp"
	"strings"
	"sync"

	"github.com/cee-project/cee/entities"
)

var tokenPattern = regexp.MustCompile(`[\p{L}\p{N}]+`)

func tokenize(text string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, tok := range tokenPattern.FindAllString(strings.ToLower(text), -1) {
		tokens[tok] = struct{}{}
	}
	return tokens
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for tok := range a {
		if _, ok := b[tok]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	return float64(intersection) / float64(union)
}

// Vectorizer turns text into an embedding. A real implementation (see
// embedhttp) calls an embeddings endpoint; matching becomes cosine
// similarity over these vectors, so "unusual sign-in location" can match
// "suspicious login" despite sharing no tokens.
type Vectorizer interface {
	Vectorize(text string) ([]float64, error)
}

// Router matches free text to a domain's registered intents. Domains never
// see each other's candidates: matching is always scoped to one DomainID,
// so two unrelated domains can register similarly-worded intents without
// interfering with each other.
type Router struct {
	threshold  float64
	nodes      map[string][]entities.IntentNode // domainID -> nodes
	vectorizer Vectorizer

	mu    sync.Mutex
	cache map[string][]float64 // example/query text -> embedding (populated lazily)
}

func NewRouter(threshold float64) *Router {
	return &Router{
		threshold: threshold,
		nodes:     make(map[string][]entities.IntentNode),
		cache:     make(map[string][]float64),
	}
}

// SetVectorizer switches the router from token-overlap to embedding-based
// semantic matching. Passing nil (the default) keeps the lexical scaffold.
// Example embeddings are computed lazily on first Match and cached.
func (r *Router) SetVectorizer(v Vectorizer) {
	r.vectorizer = v
}

func (r *Router) RegisterNode(node entities.IntentNode) {
	r.nodes[node.DomainID] = append(r.nodes[node.DomainID], node)
}

// Match returns the best-scoring intent within domainID, if any clears the
// router's threshold. An unmatched result is reported explicitly rather
// than guessed -- callers should fall through to the edge LLM injector.
//
// With a Vectorizer attached, scoring is cosine similarity over embeddings;
// if any embedding call fails, Match degrades to lexical scoring for that
// call rather than erroring, so a flaky embeddings endpoint never takes the
// router down (the signature has no error to return by design).
func (r *Router) Match(domainID, rawText string) entities.MatchResult {
	if r.vectorizer != nil {
		if result, ok := r.matchSemantic(domainID, rawText); ok {
			return result
		}
	}
	return r.matchLexical(domainID, rawText)
}

func (r *Router) matchLexical(domainID, rawText string) entities.MatchResult {
	queryTokens := tokenize(rawText)

	var bestNode *entities.IntentNode
	bestScore := 0.0

	for _, node := range r.nodes[domainID] {
		node := node
		for _, example := range node.Examples {
			score := jaccard(queryTokens, tokenize(example))
			if score > bestScore {
				bestScore = score
				bestNode = &node
			}
		}
	}
	return r.result(bestNode, bestScore)
}

// matchSemantic returns (result, true) on success, or (_, false) to signal
// the caller should fall back to lexical (an embedding call failed).
func (r *Router) matchSemantic(domainID, rawText string) (entities.MatchResult, bool) {
	queryVec, err := r.vectorFor(rawText)
	if err != nil {
		return entities.MatchResult{}, false
	}

	var bestNode *entities.IntentNode
	bestScore := 0.0

	for _, node := range r.nodes[domainID] {
		node := node
		for _, example := range node.Examples {
			exampleVec, err := r.vectorFor(example)
			if err != nil {
				return entities.MatchResult{}, false
			}
			score := cosine(queryVec, exampleVec)
			if score > bestScore {
				bestScore = score
				bestNode = &node
			}
		}
	}
	return r.result(bestNode, bestScore), true
}

func (r *Router) result(bestNode *entities.IntentNode, bestScore float64) entities.MatchResult {
	if bestNode != nil && bestScore >= r.threshold {
		return entities.MatchResult{
			Matched:          true,
			NodeRef:          bestNode.NodeID,
			Confidence:       bestScore,
			EntryWorkflowRef: bestNode.EntryWorkflowRef,
		}
	}
	return entities.MatchResult{Matched: false, Confidence: bestScore}
}

// vectorFor returns text's embedding, caching it. Example vectors are stable
// across calls; query vectors get cached too, which helps repeated queries.
func (r *Router) vectorFor(text string) ([]float64, error) {
	r.mu.Lock()
	if v, ok := r.cache[text]; ok {
		r.mu.Unlock()
		return v, nil
	}
	r.mu.Unlock()

	v, err := r.vectorizer.Vectorize(text)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[text] = v
	r.mu.Unlock()
	return v, nil
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
