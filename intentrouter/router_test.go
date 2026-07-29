package intentrouter

import (
	"testing"

	"github.com/cee-project/cee/entities"
)

func newTestRouter() *Router {
	r := NewRouter(0.5)
	r.RegisterNode(entities.IntentNode{
		NodeID:           "finance.duplicate_expense",
		DomainID:         "finance",
		Examples:         []string{"duplicate expense report", "same receipt submitted twice"},
		EntryWorkflowRef: "finance.flag",
	})
	r.RegisterNode(entities.IntentNode{
		NodeID:           "security.suspicious_login",
		DomainID:         "security",
		Examples:         []string{"suspicious login from unusual location", "failed login attempts spike"},
		EntryWorkflowRef: "security.contain",
	})
	return r
}

func TestMatchWithinDomain(t *testing.T) {
	r := newTestRouter()
	result := r.Match("finance", "duplicate expense report submitted again")
	if !result.Matched {
		t.Fatalf("expected a match, got %+v", result)
	}
	if result.EntryWorkflowRef != "finance.flag" {
		t.Fatalf("unexpected entry step ref: %s", result.EntryWorkflowRef)
	}
}

func TestMatchDoesNotLeakAcrossDomains(t *testing.T) {
	r := newTestRouter()
	result := r.Match("security", "duplicate expense report submitted again")
	if result.Matched {
		t.Fatalf("expected no match across domains, got %+v", result)
	}
}

func TestMatchBelowThresholdIsUnmatched(t *testing.T) {
	r := newTestRouter()
	result := r.Match("finance", "completely unrelated text about weather")
	if result.Matched {
		t.Fatalf("expected no match, got %+v", result)
	}
}
