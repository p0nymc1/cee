// Package registry implements the DomainRegistry: how a domain plugin
// registers itself with the shared runtime. The engine never imports a
// domain's code directly -- a domain hands the registry its intents,
// workflows and policies, and everything downstream is addressed by name
// only.
package registry

import (
	"cee/entities"
	"cee/execution"
	"cee/intentrouter"
)

// Domain is everything one domain plugin contributes to the shared runtime.
type Domain struct {
	Name      string
	Intents   []entities.IntentNode
	Workflows []*execution.Workflow
	Policies  []execution.CircuitBreakerPolicy
}

// Registry wires registered domains into a shared Router and Engine. New
// domains plug in without any change to the Router or Engine themselves.
type Registry struct {
	router  *intentrouter.Router
	engine  *execution.Engine
	domains map[string]Domain
}

func NewRegistry(router *intentrouter.Router, engine *execution.Engine) *Registry {
	return &Registry{router: router, engine: engine, domains: make(map[string]Domain)}
}

func (r *Registry) RegisterDomain(domain Domain) {
	for _, node := range domain.Intents {
		r.router.RegisterNode(node)
	}
	for _, workflow := range domain.Workflows {
		r.engine.RegisterWorkflow(workflow)
	}
	for _, policy := range domain.Policies {
		r.engine.RegisterPolicy(policy)
	}
	r.domains[domain.Name] = domain
}

func (r *Registry) Domains() []string {
	names := make([]string, 0, len(r.domains))
	for name := range r.domains {
		names = append(names, name)
	}
	return names
}
