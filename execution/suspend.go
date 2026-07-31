package execution

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

type Suspended struct {
	Reason string

	Audience string
}

func (s *Suspended) Error() string {
	return fmt.Sprintf("workflow suspended: %s", s.Reason)
}

func Suspend(reason string) (map[string]any, error) {
	return nil, &Suspended{Reason: reason}
}

func SuspendFor(reason, audience string) (map[string]any, error) {
	return nil, &Suspended{Reason: reason, Audience: audience}
}

type SuspensionObserver interface {
	ObserveSuspension(workflowID, stepID string)
}

type State struct {
	Pointer    string
	WorkflowID string

	StepID string
	Reason string

	Audience string
	Ctx      map[string]any
	Trace    []string
}

type Store interface {
	Save(State) error

	Load(pointer string) (State, error)

	Consume(pointer string) (State, error)

	Release(pointer string) error
}

type MemoryStore struct {
	mu     sync.Mutex
	states map[string]State
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{states: make(map[string]State)}
}

func (m *MemoryStore) Save(s State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.states[s.Pointer] = s
	return nil
}

func (m *MemoryStore) Load(pointer string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[pointer]
	if !ok {
		return State{}, fmt.Errorf("no suspended workflow for pointer %q", pointer)
	}
	return state, nil
}

func (m *MemoryStore) Delete(pointer string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.states[pointer]; !ok {
		return fmt.Errorf("no suspended workflow for pointer %q", pointer)
	}
	delete(m.states, pointer)
	return nil
}

func (m *MemoryStore) Consume(pointer string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.states[pointer]
	if !ok {
		return State{}, fmt.Errorf("no suspended workflow for pointer %q", pointer)
	}
	delete(m.states, pointer)
	return state, nil
}

func (m *MemoryStore) Release(string) error { return nil }

func (m *MemoryStore) Pending() []State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]State, 0, len(m.states))
	for _, s := range m.states {
		out = append(out, s)
	}
	return out
}

type NoSuspensionSupport struct {
	WorkflowID string
	StepID     string
}

func (e *NoSuspensionSupport) Error() string {
	return fmt.Sprintf(
		"step %q in workflow %q suspended but no Store is configured; call Engine.SetStore first",
		e.StepID, e.WorkflowID,
	)
}

type NestedSuspensionUnsupported struct {
	WorkflowID string
	StepID     string
}

func (e *NestedSuspensionUnsupported) Error() string {
	return fmt.Sprintf(
		"step %q suspended inside sub-workflow %q; suspension is only supported at the top level",
		e.StepID, e.WorkflowID,
	)
}

func newPointer() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("could not generate a resume pointer: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
