package intentrouter

import (
	"sync"
	"testing"

	"github.com/p0nymc1/cee/entities"
)

type matchLog struct {
	mu      sync.Mutex
	matched []bool
}

func (m *matchLog) ObserveMatch(domainID string, matched bool) {
	m.mu.Lock()
	m.matched = append(m.matched, matched)
	m.mu.Unlock()
}

func TestObserverSeesEveryMatchOutcome(t *testing.T) {
	r := newTestRouter()
	log := &matchLog{}
	r.SetObserver(log)

	if !r.Match("finance", "duplicate expense report submitted again").Matched {
		t.Fatal("expected a hit")
	}
	if r.Match("finance", "the weather is nice today").Matched {
		t.Fatal("expected a miss")
	}

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.matched) != 2 || !log.matched[0] || log.matched[1] {
		t.Errorf("observer saw %v, want [true false]", log.matched)
	}
}

func TestMatchWorksWithNoObserver(t *testing.T) {
	r := newTestRouter()
	if !r.Match("finance", "duplicate expense report submitted again").Matched {
		t.Error("a router with no observer must still match")
	}
}

func TestRegisteringWhileMatchingIsSafe(t *testing.T) {
	r := NewRouter(0.5)
	r.SetObserver(&matchLog{})
	r.RegisterNode(entities.IntentNode{
		NodeID: "d.seed", DomainID: "d", Examples: []string{"seed example"},
		EntryWorkflowRef: "d.seed",
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				r.Match("d", "seed example query")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 100; n++ {
			r.RegisterNode(entities.IntentNode{
				NodeID: "d.more", DomainID: "d", Examples: []string{"another example"},
				EntryWorkflowRef: "d.more",
			})
		}
	}()
	wg.Wait()
}
