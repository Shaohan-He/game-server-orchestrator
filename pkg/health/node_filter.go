package health

import (
	"context"
	"log"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"
)

// NodeFilter determines which nodes are healthy enough to schedule game servers onto.
type NodeFilter struct {
	provider HealthProvider
}

func NewNodeFilter(provider HealthProvider) *NodeFilter {
	return &NodeFilter{provider: provider}
}

// HealthyNodes returns two lists: healthy nodes and unhealthy nodes.
func (f *NodeFilter) HealthyNodes(ctx context.Context, cfg *v1alpha1.NodeHealthConfig) (healthy, unhealthy []string, err error) {
	if f.provider == nil {
		return nil, nil, nil
	}

	nodes, err := f.provider.GetNodeHealth(ctx)
	if err != nil {
		return nil, nil, err
	}

	for name, h := range nodes {
		switch h.Status {
		case "HEALTHY":
			healthy = append(healthy, name)
		case "WARNING":
			// WARNING nodes can still be used but are flagged.
			healthy = append(healthy, name)
			log.Printf("[WARN] [node_filter] %s: WARNING — %s", name, h.Reason)
		case "CRITICAL":
			unhealthy = append(unhealthy, name)
			log.Printf("[WARN] [node_filter] %s: CRITICAL (filtered out) — %s", name, h.Reason)
		default:
			healthy = append(healthy, name)
		}
	}

	return healthy, unhealthy, nil
}
