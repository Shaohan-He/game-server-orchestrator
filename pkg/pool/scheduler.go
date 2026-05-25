package pool

import (
	"sort"

	"github.com/noneedtostudy/game-server-orchestrator/api/v1alpha1"
)

// GameServerInfo holds runtime data for a candidate game server.
type GameServerInfo struct {
	Name        string
	Endpoint    string
	Node        string
	PlayerCount int32
	LatencyMs   int32
	Allocated   bool
}

// RankAndSelect sorts candidates by the given strategy and returns the best match.
// When PlayerCount is 0 (e.g. from pool-internal state without live metrics),
// strategies fall back to a deterministic alphabetical order.
func RankAndSelect(candidates []GameServerInfo, strategy v1alpha1.AllocationStrategy, regions []string) *GameServerInfo {
	if len(candidates) == 0 {
		return nil
	}

	var available []GameServerInfo
	for _, c := range candidates {
		if !c.Allocated {
			available = append(available, c)
		}
	}
	if len(available) == 0 {
		return nil
	}

	switch strategy {
	case v1alpha1.AllocationFewestPlayers:
		sort.Slice(available, func(i, j int) bool {
			if available[i].PlayerCount != available[j].PlayerCount {
				return available[i].PlayerCount < available[j].PlayerCount
			}
			return available[i].Name < available[j].Name
		})
	case v1alpha1.AllocationLowestLatency:
		sort.Slice(available, func(i, j int) bool {
			if available[i].LatencyMs != available[j].LatencyMs {
				return available[i].LatencyMs < available[j].LatencyMs
			}
			return available[i].Name < available[j].Name
		})
	case v1alpha1.AllocationRoundRobin:
		sort.Slice(available, func(i, j int) bool {
			if available[i].PlayerCount != available[j].PlayerCount {
				return available[i].PlayerCount < available[j].PlayerCount
			}
			return available[i].Name < available[j].Name
		})
	case v1alpha1.AllocationStrictBinPack:
		sort.Slice(available, func(i, j int) bool {
			if available[i].PlayerCount != available[j].PlayerCount {
				return available[i].PlayerCount > available[j].PlayerCount
			}
			return available[i].Name < available[j].Name
		})
	default:
		sort.Slice(available, func(i, j int) bool {
			if available[i].PlayerCount != available[j].PlayerCount {
				return available[i].PlayerCount < available[j].PlayerCount
			}
			return available[i].Name < available[j].Name
		})
	}

	return &available[0]
}

// SelectForDrain picks which game servers should be drained first.
// Prefers servers with the fewest active sessions to minimize impact.
func SelectForDrain(candidates []GameServerInfo, count int) []GameServerInfo {
	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].PlayerCount != candidates[j].PlayerCount {
			return candidates[i].PlayerCount < candidates[j].PlayerCount
		}
		return candidates[i].Name < candidates[j].Name
	})

	if count > len(candidates) {
		count = len(candidates)
	}
	return candidates[:count]
}
