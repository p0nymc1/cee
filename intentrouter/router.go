package intentrouter

import (
	"math"
	"regexp"
	"strings"
	"sync"

	"github.com/p0nymc1/cee/entities"
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

type Vectorizer interface {
	Vectorize(text string) ([]float64, error)
}

type Router struct {
	threshold  float64
	nodes      map[string][]entities.IntentNode
	vectorizer Vectorizer

	mu    sync.Mutex
	cache map[string][]float64
}

func NewRouter(threshold float64) *Router {
	return &Router{
		threshold: threshold,
		nodes:     make(map[string][]entities.IntentNode),
		cache:     make(map[string][]float64),
	}
}

func (r *Router) SetVectorizer(v Vectorizer) {
	r.vectorizer = v
}

func (r *Router) RegisterNode(node entities.IntentNode) {
	r.nodes[node.DomainID] = append(r.nodes[node.DomainID], node)
}

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
