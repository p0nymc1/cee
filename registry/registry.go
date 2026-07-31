package registry

import (
	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
)

type Domain struct {
	Name      string
	Intents   []entities.IntentNode
	Workflows []*execution.Workflow
	Policies  []execution.CircuitBreakerPolicy
}

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

		workflow.DomainID = domain.Name
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
