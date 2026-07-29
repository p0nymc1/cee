// Package intentrouter implements CEE's intent routing layer: a
// lightweight, dependency-free scaffold that matches free text against each
// domain's registered IntentNodes using token-overlap similarity. Swap in a
// real embedding model + vector database behind the same Router API once a
// domain's node library outgrows token overlap matching.
package intentrouter

import (
	"regexp"
	"strings"

	"cee/entities"
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

// Router matches free text to a domain's registered intents. Domains never
// see each other's candidates: matching is always scoped to one DomainID,
// so two unrelated domains can register similarly-worded intents without
// interfering with each other.
type Router struct {
	threshold float64
	nodes     map[string][]entities.IntentNode // domainID -> nodes
}

func NewRouter(threshold float64) *Router {
	return &Router{threshold: threshold, nodes: make(map[string][]entities.IntentNode)}
}

func (r *Router) RegisterNode(node entities.IntentNode) {
	r.nodes[node.DomainID] = append(r.nodes[node.DomainID], node)
}

// Match returns the best-scoring intent within domainID, if any clears the
// router's threshold. An unmatched result is reported explicitly rather
// than guessed -- callers should fall through to the edge LLM injector.
func (r *Router) Match(domainID, rawText string) entities.MatchResult {
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

	if bestNode != nil && bestScore >= r.threshold {
		return entities.MatchResult{
			Matched:      true,
			NodeRef:      bestNode.NodeID,
			Confidence:   bestScore,
			EntryStepRef: bestNode.EntryStepRef,
		}
	}
	return entities.MatchResult{Matched: false, Confidence: bestScore}
}
