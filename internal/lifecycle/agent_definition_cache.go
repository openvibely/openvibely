package lifecycle

import (
	"context"
	"strings"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
)

// AgentDefinitionCache is a turn-scoped Agent definition lookup. It memoizes
// both successful and failed lookups so concurrent hooks cannot reload the same
// definition or repeatedly retry a missing/broken row during one turn.
type AgentDefinitionCache struct {
	lookup AgentLookup

	mu      sync.Mutex
	entries map[string]agentDefinitionResult
}

type agentDefinitionResult struct {
	agent *models.Agent
	err   error
}

// NewAgentDefinitionCache creates an isolated cache for one lifecycle turn.
// The cache has no process-wide state and must not be retained between turns.
func NewAgentDefinitionCache(lookup AgentLookup) *AgentDefinitionCache {
	return &AgentDefinitionCache{
		lookup:  lookup,
		entries: make(map[string]agentDefinitionResult),
	}
}

// Seed adds a definition already loaded by the caller to the turn cache.
func (c *AgentDefinitionCache) Seed(agent *models.Agent) {
	if c == nil || agent == nil || strings.TrimSpace(agent.ID) == "" {
		return
	}
	c.mu.Lock()
	c.entries[agent.ID] = agentDefinitionResult{agent: agent}
	c.mu.Unlock()
}

// SetLookupIfMissing supplies a fallback lookup for callers that created the
// turn cache before their Agent repository was available. It never replaces a
// lookup already bound to the turn.
func (c *AgentDefinitionCache) SetLookupIfMissing(lookup AgentLookup) {
	if c == nil || lookup == nil {
		return
	}
	c.mu.Lock()
	if c.lookup == nil {
		c.lookup = lookup
	}
	c.mu.Unlock()
}

// the read-through operation single-flight for each turn, including hooks that
// start concurrently in a non-blocking lifecycle slot.
func (c *AgentDefinitionCache) GetByID(ctx context.Context, id string) (*models.Agent, error) {
	if c == nil {
		return nil, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if result, ok := c.entries[id]; ok {
		return result.agent, result.err
	}
	if c.lookup == nil {
		c.entries[id] = agentDefinitionResult{}
		return nil, nil
	}
	agent, err := c.lookup.GetByID(ctx, id)
	c.entries[id] = agentDefinitionResult{agent: agent, err: err}
	return agent, err
}

type agentDefinitionCacheContextKey struct{}
type assignedAgentDefinitionContextKey struct{}

// WithAgentDefinitionCache attaches a cache to a lifecycle turn context.
func WithAgentDefinitionCache(ctx context.Context, cache *AgentDefinitionCache) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, agentDefinitionCacheContextKey{}, cache)
}

// AgentDefinitionCacheFromContext returns the cache attached to the current
// lifecycle turn, if any.
func AgentDefinitionCacheFromContext(ctx context.Context) *AgentDefinitionCache {
	if ctx == nil {
		return nil
	}
	cache, _ := ctx.Value(agentDefinitionCacheContextKey{}).(*AgentDefinitionCache)
	return cache
}

// WithAssignedAgentDefinition hands a definition resolved by an outer task
// handler to PrepareLifecycleTurn without making it process-global.
func WithAssignedAgentDefinition(ctx context.Context, agent *models.Agent) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, assignedAgentDefinitionContextKey{}, agent)
}

// AssignedAgentDefinitionFromContext returns an outer-handler Agent handoff.
func AssignedAgentDefinitionFromContext(ctx context.Context) *models.Agent {
	if ctx == nil {
		return nil
	}
	agent, _ := ctx.Value(assignedAgentDefinitionContextKey{}).(*models.Agent)
	return agent
}
