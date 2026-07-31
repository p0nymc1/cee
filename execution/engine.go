package execution

import (
	"errors"
	"fmt"

	"github.com/p0nymc1/cee/entities"
)

type CircuitBreakerTripped struct {
	StepID string
	Reason string

	Compensated          []string
	CompensationFailures []CompensationFailure
}

func (e *CircuitBreakerTripped) Error() string {
	return fmt.Sprintf("circuit breaker tripped at step %q: %s%s",
		e.StepID, e.Reason, compensationSummary(e.Compensated, e.CompensationFailures))
}

const (
	DefaultMaxSteps = 10_000
	DefaultMaxDepth = 64
)

type StepLimitExceeded struct {
	WorkflowID  string
	Limit       int
	RecentSteps []string
}

func (e *StepLimitExceeded) Error() string {
	return fmt.Sprintf(
		"workflow %q exceeded the %d step limit (likely an on_success cycle); most recent steps: %v",
		e.WorkflowID, e.Limit, e.RecentSteps,
	)
}

type DepthLimitExceeded struct {
	WorkflowID string
	Limit      int
}

func (e *DepthLimitExceeded) Error() string {
	return fmt.Sprintf(
		"workflow %q exceeded the %d sub-workflow nesting limit (likely a sub_workflow_ref cycle)",
		e.WorkflowID, e.Limit,
	)
}

type Step interface {
	ID() string
	circuitBreakerPolicyRef() string
}

type Action func(ctx map[string]any) (map[string]any, error)

type LeafStep struct {
	StepID                  string
	Run                     Action
	SandboxProbeRef         string
	CircuitBreakerPolicyRef string
	OnSuccess               string

	CompensateStepRef string
}

func (s *LeafStep) ID() string                      { return s.StepID }
func (s *LeafStep) circuitBreakerPolicyRef() string { return s.CircuitBreakerPolicyRef }

type CompositeStep struct {
	StepID                  string
	SubWorkflowRef          string
	CircuitBreakerPolicyRef string
	OnSuccess               string
}

func (s *CompositeStep) ID() string                      { return s.StepID }
func (s *CompositeStep) circuitBreakerPolicyRef() string { return s.CircuitBreakerPolicyRef }

type CircuitBreakerPolicy struct {
	PolicyID        string
	FallbackStepRef string
}

type Workflow struct {
	WorkflowID  string
	DomainID    string
	EntryStepID string
	Steps       map[string]Step
}

type Prober interface {
	Probe(entities.ProbeRequest) (entities.ProbeResult, error)
}

type Observer interface {
	ObserveStep(workflowID, stepID string)
	ObserveSandboxProbe(workflowID, stepID string)
	ObserveCircuitBreaker(workflowID, stepID string)
}

type Engine struct {
	sandbox    Prober
	observer   Observer
	workflows  map[string]*Workflow
	policies   map[string]CircuitBreakerPolicy
	store      Store
	authorizer Authorizer
	maxSteps   int
	maxDepth   int
}

func (e *Engine) SetStore(s Store) {
	e.store = s
}

func NewEngine(sandbox Prober) *Engine {
	return &Engine{
		sandbox:   sandbox,
		workflows: make(map[string]*Workflow),
		policies:  make(map[string]CircuitBreakerPolicy),
		maxSteps:  DefaultMaxSteps,
		maxDepth:  DefaultMaxDepth,
	}
}

func (e *Engine) SetLimits(maxSteps, maxDepth int) {
	if maxSteps > 0 {
		e.maxSteps = maxSteps
	}
	if maxDepth > 0 {
		e.maxDepth = maxDepth
	}
}

func (e *Engine) SetObserver(o Observer) {
	e.observer = o
}

func (e *Engine) RegisterWorkflow(w *Workflow) {
	e.workflows[w.WorkflowID] = w
}

func (e *Engine) RegisterPolicy(p CircuitBreakerPolicy) {
	e.policies[p.PolicyID] = p
}

func (e *Engine) Run(workflowRef string, ctx map[string]any) (entities.WorkflowResult, error) {
	return e.run(workflowRef, ctx, 0)
}

func (e *Engine) run(workflowRef string, ctx map[string]any, depth int) (entities.WorkflowResult, error) {
	return e.runFrom(workflowRef, "", ctx, depth)
}

func (e *Engine) runFrom(workflowRef, startStepID string, ctx map[string]any, depth int) (entities.WorkflowResult, error) {
	if depth > e.maxDepth {
		return entities.WorkflowResult{}, &DepthLimitExceeded{WorkflowID: workflowRef, Limit: e.maxDepth}
	}

	workflow, ok := e.workflows[workflowRef]
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf("no workflow registered for %q", workflowRef)
	}

	stepID := startStepID
	if stepID == "" {
		stepID = workflow.EntryStepID
	}

	var done []completed
	var trace []string
	steps := 0

	for stepID != "" {
		steps++
		if steps > e.maxSteps {
			return entities.WorkflowResult{}, &StepLimitExceeded{
				WorkflowID:  workflowRef,
				Limit:       e.maxSteps,
				RecentSteps: tail(trace, 10),
			}
		}

		step, ok := workflow.Steps[stepID]
		if !ok {
			return entities.WorkflowResult{}, fmt.Errorf("workflow %q has no step %q", workflowRef, stepID)
		}
		trace = append(trace, step.ID())

		switch s := step.(type) {
		case *CompositeStep:
			subResult, err := e.run(s.SubWorkflowRef, ctx, depth+1)
			if err != nil {

				if bypassesBreaker(err) {
					return entities.WorkflowResult{}, err
				}
				e.observe(func(o Observer) { o.ObserveCircuitBreaker(workflow.WorkflowID, s.StepID) })
				next, nextCtx, breakErr := e.onFailure(s, err.Error(), ctx)
				if breakErr != nil {
					return entities.WorkflowResult{}, e.abandon(workflow, done, ctx, breakErr)
				}
				ctx = nextCtx
				stepID = next
				continue
			}
			trace = append(trace, subResult.Trace...)
			ctx = merge(ctx, subResult.Output)
			stepID = s.OnSuccess

		case *LeafStep:
			if s.SandboxProbeRef != "" {
				if e.sandbox == nil {
					return entities.WorkflowResult{}, fmt.Errorf(
						"step %q declares sandbox_probe_ref %q but no sandbox is configured",
						s.StepID, s.SandboxProbeRef,
					)
				}
				e.observe(func(o Observer) { o.ObserveSandboxProbe(workflow.WorkflowID, s.StepID) })
				probeResult, err := e.sandbox.Probe(entities.ProbeRequest{
					ProbeRef:    s.SandboxProbeRef,
					DomainID:    workflow.DomainID,
					StepContext: ctx,
				})
				if err != nil || !probeResult.Healthy {
					reason := probeResult.DetectedFailureMode
					if err != nil {
						reason = err.Error()
					}
					e.observe(func(o Observer) { o.ObserveCircuitBreaker(workflow.WorkflowID, s.StepID) })
					next, nextCtx, breakErr := e.onFailure(s, reason, ctx)
					if breakErr != nil {
						return entities.WorkflowResult{}, e.abandon(workflow, done, ctx, breakErr)
					}
					ctx = nextCtx
					stepID = next
					continue
				}
			}

			e.observe(func(o Observer) { o.ObserveStep(workflow.WorkflowID, s.StepID) })
			output, err := s.Run(ctx)
			if err != nil {

				var suspended *Suspended
				if errors.As(err, &suspended) {
					return e.suspend(workflow, s.StepID, suspended, ctx, trace, depth)
				}
				e.observe(func(o Observer) { o.ObserveCircuitBreaker(workflow.WorkflowID, s.StepID) })
				next, nextCtx, breakErr := e.onFailure(s, err.Error(), ctx)
				if breakErr != nil {
					return entities.WorkflowResult{}, e.abandon(workflow, done, ctx, breakErr)
				}
				ctx = nextCtx
				stepID = next
				continue
			}
			ctx = merge(ctx, output)
			if s.CompensateStepRef != "" {
				done = append(done, completed{stepID: s.StepID, compensateStep: s.CompensateStepRef})
			}
			stepID = s.OnSuccess

		default:
			return entities.WorkflowResult{}, fmt.Errorf("unknown step type for %q", stepID)
		}
	}

	return entities.WorkflowResult{Output: ctx, Trace: trace}, nil
}

func (e *Engine) suspend(
	workflow *Workflow, stepID string, s *Suspended,
	ctx map[string]any, trace []string, depth int,
) (entities.WorkflowResult, error) {
	if depth > 0 {
		return entities.WorkflowResult{}, &NestedSuspensionUnsupported{
			WorkflowID: workflow.WorkflowID, StepID: stepID,
		}
	}
	if e.store == nil {
		return entities.WorkflowResult{}, &NoSuspensionSupport{
			WorkflowID: workflow.WorkflowID, StepID: stepID,
		}
	}

	pointer, err := newPointer()
	if err != nil {
		return entities.WorkflowResult{}, err
	}
	state := State{
		Pointer:    pointer,
		WorkflowID: workflow.WorkflowID,
		StepID:     stepID,
		Reason:     s.Reason,
		Audience:   s.Audience,
		Ctx:        ctx,
		Trace:      trace,
	}
	if err := e.store.Save(state); err != nil {
		return entities.WorkflowResult{}, fmt.Errorf("could not save suspended workflow: %w", err)
	}

	if so, ok := e.observer.(SuspensionObserver); ok {
		so.ObserveSuspension(workflow.WorkflowID, stepID)
	}
	return entities.WorkflowResult{Output: ctx, StatePointer: pointer, Trace: trace}, nil
}

func (e *Engine) Resume(pointer string, resolution map[string]any) (entities.WorkflowResult, error) {
	return e.ResumeAs(pointer, "", resolution)
}

func (e *Engine) ResumeAs(pointer, identity string, resolution map[string]any) (entities.WorkflowResult, error) {
	if e.store == nil {
		return entities.WorkflowResult{}, fmt.Errorf("no Store is configured; call Engine.SetStore first")
	}
	state, err := e.store.Load(pointer)
	if err != nil {
		return entities.WorkflowResult{}, err
	}

	workflow, ok := e.workflows[state.WorkflowID]
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf(
			"suspended workflow %q is no longer registered", state.WorkflowID)
	}
	step, ok := workflow.Steps[state.StepID]
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf(
			"workflow %q no longer has the suspended step %q", state.WorkflowID, state.StepID)
	}
	leaf, ok := step.(*LeafStep)
	if !ok {
		return entities.WorkflowResult{}, fmt.Errorf(
			"suspended step %q is no longer a leaf step", state.StepID)
	}

	if err := e.authorize(state, identity); err != nil {
		return entities.WorkflowResult{}, err
	}

	claimed, err := e.store.Consume(pointer)
	if err != nil {
		return entities.WorkflowResult{}, fmt.Errorf("could not claim resume pointer: %w", err)
	}

	resumeCtx := merge(claimed.Ctx, resolution)
	if identity != "" {
		resumeCtx[ResumedByKey] = identity
	}
	result, err := e.runFrom(claimed.WorkflowID, leaf.OnSuccess, resumeCtx, 0)

	if releaseErr := e.store.Release(pointer); releaseErr != nil && err == nil {
		return result, fmt.Errorf("run finished but its claim could not be released: %w", releaseErr)
	}

	result.Trace = append(append([]string{}, claimed.Trace...), result.Trace...)
	return result, err
}

func (e *Engine) observe(fn func(Observer)) {
	if e.observer != nil {
		fn(e.observer)
	}
}

const (
	FailureReasonKey = "cee.failure_reason"

	FailedStepKey = "cee.failed_step"
)

func (e *Engine) onFailure(step Step, reason string, ctx map[string]any) (string, map[string]any, error) {
	if ref := step.circuitBreakerPolicyRef(); ref != "" {
		if policy, ok := e.policies[ref]; ok && policy.FallbackStepRef != "" {
			return policy.FallbackStepRef, merge(ctx, map[string]any{
				FailureReasonKey: reason,
				FailedStepKey:    step.ID(),
			}), nil
		}
	}
	return "", ctx, &CircuitBreakerTripped{StepID: step.ID(), Reason: reason}
}

func bypassesBreaker(err error) bool {
	var stepLimit *StepLimitExceeded
	var depthLimit *DepthLimitExceeded
	var nested *NestedSuspensionUnsupported
	var noStore *NoSuspensionSupport
	return errors.As(err, &stepLimit) ||
		errors.As(err, &depthLimit) ||
		errors.As(err, &nested) ||
		errors.As(err, &noStore)
}

func tail(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func merge(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}
