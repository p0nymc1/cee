package execution

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// Suspended is returned by an Action to pause a workflow pending something
// outside the engine -- a human decision, a callback, a scheduled window.
// It is deliberately NOT a failure: the circuit breaker never sees it, no
// fallback runs, and nothing is retried. The engine saves the run's context
// and hands the caller a pointer to resume from.
//
// Returning a value as an error follows the fs.SkipDir precedent: it is a
// control signal the caller is expected to recognise by type, not a fault.
type Suspended struct {
	// Reason is a human-readable account of what is being waited on. It is
	// recorded in the saved state so an operator listing pending runs can
	// see why each one is parked.
	Reason string

	// Audience optionally names who may answer this suspension -- a role, a
	// team, a queue. It is opaque to the engine, which only passes it to the
	// domain's Authorizer. Leaving it empty means anyone holding the pointer
	// may resume, which is the right default for a workflow whose pointer
	// never leaves the process, and the wrong one the moment it does.
	Audience string
}

func (s *Suspended) Error() string {
	return fmt.Sprintf("workflow suspended: %s", s.Reason)
}

// Suspend is shorthand for returning a suspension from an Action.
func Suspend(reason string) (map[string]any, error) {
	return nil, &Suspended{Reason: reason}
}

// SuspendFor is Suspend with an audience: only identities the domain's
// Authorizer accepts for that audience may resume the run.
func SuspendFor(reason, audience string) (map[string]any, error) {
	return nil, &Suspended{Reason: reason, Audience: audience}
}

// SuspensionObserver is an optional extension of Observer. An Observer that
// also implements it is notified when a run parks. Kept separate rather than
// folded into Observer so adding it does not break existing implementations
// -- the engine type-asserts for it and skips the callback when absent.
type SuspensionObserver interface {
	ObserveSuspension(workflowID, stepID string)
}

// State is everything needed to pick a suspended run back up. It is a value
// type with no engine pointers, so a Store implementation is free to
// serialise it to disk or a database.
type State struct {
	Pointer    string
	WorkflowID string
	// StepID is the step that suspended. Resuming continues from that
	// step's OnSuccess -- the suspension point itself is considered done
	// once the external event it waited on has arrived.
	StepID string
	Reason string
	// Audience is carried from the Suspended that created this state, so the
	// rule about who may answer survives a restart along with the run.
	Audience string
	Ctx      map[string]any
	Trace    []string
}

// Store is where suspended runs live between Run and Resume. The default is
// in-memory; swap in a durable implementation to survive a restart. Mirrors
// the Prober arrangement: the engine depends on the interface, never on a
// particular backend.
type Store interface {
	Save(State) error

	// Load reads a parked run without claiming it. The engine uses it to
	// check a run is still resumable before committing to it, so a pointer
	// is never destroyed just because a deployment moved on.
	Load(pointer string) (State, error)

	// Consume atomically claims a parked run: it returns the state and
	// removes it in one indivisible step, so that when several processes
	// race to resume the same pointer exactly one of them can win and the
	// rest get an error.
	//
	// This is deliberately the only way the engine takes a pointer -- there
	// is no plain Delete on this interface, because a load-then-delete pair
	// looks correct and silently permits a decision to be applied twice
	// under concurrency. Making the atomic operation the only operation
	// means an implementation cannot get that wrong by accident.
	//
	// Consume claims but does not discard: the claim is held until Release.
	// A process that dies between the two leaves a claim behind on purpose,
	// so a run that was taken and never finished is discoverable afterwards
	// instead of vanishing.
	Consume(pointer string) (State, error)

	// Release discards a claim once the resumed run has finished, whether it
	// finished successfully or not -- either way the decision has been acted
	// on and must not be acted on again.
	//
	// A claim that is never released is the signature of a process that died
	// mid-run. Implementations should keep those visible rather than
	// requeueing them: the engine offers no idempotency, so re-running a
	// workflow whose side effects may already have landed is worse than
	// telling an operator that one needs a decision.
	Release(pointer string) error
}

// MemoryStore is the default Store: process-local and safe for concurrent
// use, but lost on restart. Enough to develop and test a suspending
// workflow without standing up any infrastructure.
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

// Consume claims a parked run. The lookup and the removal happen under one
// lock, so concurrent callers cannot both walk away with the same state.
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

// Release is a no-op here. The two-phase claim exists so a claim outlives the
// process that took it; this store dies with its process, so there is nothing
// a held claim could later be recovered from. Pretending otherwise would
// offer a durability guarantee an in-memory map cannot keep.
func (m *MemoryStore) Release(string) error { return nil }

// Pending lists the pointers currently held, for operator tooling.
func (m *MemoryStore) Pending() []State {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]State, 0, len(m.states))
	for _, s := range m.states {
		out = append(out, s)
	}
	return out
}

// NoSuspensionSupport is returned when a workflow suspends but the engine
// has no Store attached. Failing loudly beats silently treating a
// suspension as an ordinary failure and letting a breaker swallow it.
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

// NestedSuspensionUnsupported is returned when a step inside a sub-workflow
// suspends. Resuming would have to restore the whole composite call stack,
// which the engine does not yet record, so this is rejected outright rather
// than resumed halfway into a state nobody can reason about.
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

// newPointer mints an opaque, unguessable resume token. Opaque so callers
// cannot infer one pointer from another and reach a run that is not theirs.
func newPointer() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("could not generate a resume pointer: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
