package connector

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Registry maps a connector type to its implementation, and does nothing else.
//
// It exists because the connections table is generic: a row names its connector
// type as a string, and something has to turn that string into the code that
// understands it. One map lookup is the whole mechanism -- no plugin loading,
// no reflection, no lifecycle.
//
// It does not decode configurations. Turning stored bytes into a usable
// configuration is the connector's job, through DecodeConfig, and deciding when
// that happens is the caller's -- in this application it is a named step in the
// workflow. A registry that also decoded would be two jobs in one type.
type Registry struct {
	mu         sync.RWMutex
	connectors map[Type]Connector
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{connectors: make(map[Type]Connector)}
}

// Register adds a connector. It panics on a duplicate, because registration
// happens at startup and a duplicate is a programming error rather than a
// runtime condition.
func (r *Registry) Register(c Connector) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if c == nil {
		panic("connector: cannot register a nil connector")
	}
	if _, exists := r.connectors[c.Type()]; exists {
		panic("connector: duplicate registration for " + string(c.Type()))
	}
	r.connectors[c.Type()] = c
}

// Get returns the connector registered for t.
func (r *Registry) Get(t Type) (Connector, error) {
	r.mu.RLock()
	c, ok := r.connectors[t]
	r.mu.RUnlock()

	if !ok {
		// The message names the field a caller would have to change and lists
		// what is on offer, because the registry is the only thing that knows
		// what is registered.
		return nil, InvalidConfig("connectorType",
			fmt.Sprintf("There is no %q connector. Supported types: %s.", t, r.names()))
	}
	return c, nil
}

// Types lists the registered connector types, sorted for stable output.
func (r *Registry) Types() []Type {
	r.mu.RLock()
	defer r.mu.RUnlock()

	types := make([]Type, 0, len(r.connectors))
	for t := range r.connectors {
		types = append(types, t)
	}
	slices.Sort(types)
	return types
}

// names lists the registered types for a message.
func (r *Registry) names() string {
	types := r.Types()
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}
