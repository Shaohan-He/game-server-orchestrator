package pool

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/noneedtostudy/game-server-orchestrator/api/v1alpha1"
	"github.com/noneedtostudy/game-server-orchestrator/pkg/health"
)

// FleetPoolConfig holds per-fleet configuration used by the pool manager.
type FleetPoolConfig struct {
	BufferSize  int32
	IdleTimeout time.Duration
}

// PoolManager tracks game server pool state on a per-fleet basis.
// Each fleet has its own isolated pool of servers.
type PoolManager struct {
	mu              sync.Mutex
	pools           map[string]map[string]*ServerPoolState // fleetName -> serverName -> state
	configs         map[string]FleetPoolConfig              // fleetName -> config
	roundRobinIdx   map[string]int                         // fleetName -> next index for round-robin
	nodeFilter      *health.NodeFilter
}

// ServerPoolState tracks the runtime state of a single game server in a pool.
type ServerPoolState struct {
	Name            string
	Allocated       bool
	Healthy         bool
	LastUsedAt      time.Time
	AllocationCount int32
}

func NewPoolManager(nf *health.NodeFilter) *PoolManager {
	return &PoolManager{
		pools:         make(map[string]map[string]*ServerPoolState),
		configs:       make(map[string]FleetPoolConfig),
		roundRobinIdx: make(map[string]int),
		nodeFilter:    nf,
	}
}

// UpdateFleetConfig sets or updates the per-fleet configuration.
func (pm *PoolManager) UpdateFleetConfig(fleetName string, cfg FleetPoolConfig) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.configs[fleetName] = cfg
}

// ensureFleet creates the per-fleet pool map if it doesn't exist.
// Must be called with pm.mu held.
func (pm *PoolManager) ensureFleetLocked(fleetName string) {
	if _, ok := pm.pools[fleetName]; !ok {
		pm.pools[fleetName] = make(map[string]*ServerPoolState)
	}
}

// Register adds a game server to a fleet's pool.
func (pm *PoolManager) Register(fleetName, name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.ensureFleetLocked(fleetName)
	if _, ok := pm.pools[fleetName][name]; !ok {
		pm.pools[fleetName][name] = &ServerPoolState{
			Name:       name,
			Allocated:  false,
			Healthy:    true,
			LastUsedAt: time.Now(),
		}
	}
}

// Allocate marks a server as allocated and returns the server name.
// Uses the given strategy to select among available servers.
// PoolManager uses AllocationCount as a proxy for player load when live metrics
// are not available — this provides a reasonable spread for FewestPlayers and
// StrictBinPack strategies without requiring an external metrics scrape on every
// allocation.
func (pm *PoolManager) Allocate(fleetName string, strategy v1alpha1.AllocationStrategy) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pool := pm.pools[fleetName]
	if pool == nil {
		return "", fmt.Errorf("no pool for fleet %s", fleetName)
	}

	// Collect eligible candidates.
	candidates := pm.collectCandidatesLocked(fleetName)
	if len(candidates) == 0 {
		return "", fmt.Errorf("no available game server in pool for fleet %s", fleetName)
	}

	// Round-robin is stateful and handled separately.
	if strategy == v1alpha1.AllocationRoundRobin {
		// Sort for deterministic ordering.
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Name < candidates[j].Name
		})
		idx := pm.roundRobinIdx[fleetName] % len(candidates)
		chosen := candidates[idx]
		pm.roundRobinIdx[fleetName] = idx + 1

		pool[chosen.Name].Allocated = true
		pool[chosen.Name].LastUsedAt = time.Now()
		pool[chosen.Name].AllocationCount++
		return chosen.Name, nil
	}

	// Use strategy-based ranking for all other strategies.
	selected := RankAndSelect(candidates, strategy, nil)
	if selected == nil {
		return "", fmt.Errorf("no available game server in pool for fleet %s", fleetName)
	}

	pool[selected.Name].Allocated = true
	pool[selected.Name].LastUsedAt = time.Now()
	pool[selected.Name].AllocationCount++
	return selected.Name, nil
}

// collectCandidatesLocked builds a GameServerInfo slice from available servers.
// Must be called with pm.mu held.
func (pm *PoolManager) collectCandidatesLocked(fleetName string) []GameServerInfo {
	pool := pm.pools[fleetName]
	if pool == nil {
		return nil
	}
	var candidates []GameServerInfo
	for name, s := range pool {
		if !s.Allocated && s.Healthy {
			candidates = append(candidates, GameServerInfo{
				Name:        name,
				PlayerCount: s.AllocationCount,
				Allocated:   false,
			})
		}
	}
	return candidates
}

// Release marks a server as unallocated (returned to buffer).
func (pm *PoolManager) Release(fleetName, name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pool := pm.pools[fleetName]; pool != nil {
		if s, ok := pool[name]; ok {
			s.Allocated = false
			s.LastUsedAt = time.Now()
		}
	}
}

// MarkAllocated directly sets a server as allocated by name.
// Used when rebuilding pool state from Pod labels after a controller restart.
func (pm *PoolManager) MarkAllocated(fleetName, name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.ensureFleetLocked(fleetName)
	if s, ok := pm.pools[fleetName][name]; ok {
		s.Allocated = true
		s.LastUsedAt = time.Now()
		s.AllocationCount++
	}
}

// MarkAvailable resets a server to the healthy, unallocated state.
func (pm *PoolManager) MarkAvailable(fleetName, name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.ensureFleetLocked(fleetName)
	if s, ok := pm.pools[fleetName][name]; ok {
		s.Allocated = false
		s.Healthy = true
		s.LastUsedAt = time.Now()
	}
}

// MarkUnhealthy removes a server from the available pool for a fleet.
func (pm *PoolManager) MarkUnhealthy(fleetName, name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.ensureFleetLocked(fleetName)
	if s, ok := pm.pools[fleetName][name]; ok {
		s.Healthy = false
	}
}

// Remove deletes a server from a fleet's pool tracking entirely.
func (pm *PoolManager) Remove(fleetName, name string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if pool := pm.pools[fleetName]; pool != nil {
		delete(pool, name)
	}
}

// Stats returns current pool statistics for a fleet.
func (pm *PoolManager) Stats(fleetName string) PoolStats {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.statsLocked(fleetName)
}

func (pm *PoolManager) statsLocked(fleetName string) PoolStats {

	var stats PoolStats
	pool := pm.pools[fleetName]
	if pool == nil {
		return stats
	}

	stats.Total = int32(len(pool))
	for _, s := range pool {
		if s.Healthy {
			stats.Healthy++
			if !s.Allocated {
				stats.BufferAvailable++
			} else {
				stats.Allocated++
			}
		} else {
			stats.Unhealthy++
		}
	}
	return stats
}

// IdleServers returns names of servers in a fleet's pool that have been idle beyond the timeout,
// ordered by AllocationCount ascending so servers with the least usage are drained first.
func (pm *PoolManager) IdleServers(fleetName string) []string {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	cfg := pm.configs[fleetName]
	pool := pm.pools[fleetName]
	if pool == nil || cfg.IdleTimeout <= 0 {
		return nil
	}

	now := time.Now()
	var candidates []GameServerInfo
	for name, s := range pool {
		if !s.Allocated && s.Healthy {
			if now.Sub(s.LastUsedAt) > cfg.IdleTimeout {
				candidates = append(candidates, GameServerInfo{
					Name:        name,
					PlayerCount: s.AllocationCount,
				})
			}
		}
	}

	selected := SelectForDrain(candidates, len(candidates))
	idle := make([]string, 0, len(selected))
	for _, s := range selected {
		idle = append(idle, s.Name)
	}
	return idle
}

// NeedsRefill returns true if the fleet's buffer pool is below target.
func (pm *PoolManager) NeedsRefill(fleetName string) bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	stats := pm.statsLocked(fleetName)
	cfg := pm.configs[fleetName]
	return stats.BufferAvailable < cfg.BufferSize
}

// RefillCount returns how many servers to create to fill the fleet's buffer.
func (pm *PoolManager) RefillCount(fleetName string) int32 {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	stats := pm.statsLocked(fleetName)
	cfg := pm.configs[fleetName]
	needed := cfg.BufferSize - stats.BufferAvailable
	if needed < 0 {
		return 0
	}
	return needed
}

// PoolStats is a snapshot of a fleet's pool statistics.
type PoolStats struct {
	Total           int32
	Healthy         int32
	Unhealthy       int32
	Allocated       int32
	BufferAvailable int32
}

// FilterNodesByHealth returns the subset of candidate nodes that are healthy.
func (pm *PoolManager) FilterNodesByHealth(ctx context.Context, candidates []string, nodeHealthCfg *v1alpha1.NodeHealthConfig) ([]string, error) {
	if pm.nodeFilter == nil || nodeHealthCfg == nil || !nodeHealthCfg.Enabled {
		return candidates, nil
	}

	healthy, _, err := pm.nodeFilter.HealthyNodes(ctx, nodeHealthCfg)
	if err != nil {
		log.Printf("[WARN] [pool] node health filter failed: %v, using all candidates", err)
		return candidates, nil
	}

	healthySet := make(map[string]bool, len(healthy))
	for _, n := range healthy {
		healthySet[n] = true
	}

	var filtered []string
	for _, c := range candidates {
		if healthySet[c] {
			filtered = append(filtered, c)
		}
	}
	return filtered, nil
}

// AllocateWithFilter is like Allocate but only considers servers whose names appear in
// allowedNames. An empty or nil allowedNames falls back to the full candidate pool.
func (pm *PoolManager) AllocateWithFilter(fleetName string, strategy v1alpha1.AllocationStrategy, allowedNames map[string]bool) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pool := pm.pools[fleetName]
	if pool == nil {
		return "", fmt.Errorf("no pool for fleet %s", fleetName)
	}

	candidates := pm.collectCandidatesLocked(fleetName)
	if len(allowedNames) > 0 {
		filtered := make([]GameServerInfo, 0, len(candidates))
		for _, c := range candidates {
			if allowedNames[c.Name] {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no available game server in pool for fleet %s", fleetName)
	}

	if strategy == v1alpha1.AllocationRoundRobin {
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Name < candidates[j].Name
		})
		idx := pm.roundRobinIdx[fleetName] % len(candidates)
		chosen := candidates[idx]
		pm.roundRobinIdx[fleetName] = idx + 1

		pool[chosen.Name].Allocated = true
		pool[chosen.Name].LastUsedAt = time.Now()
		pool[chosen.Name].AllocationCount++
		return chosen.Name, nil
	}

	selected := RankAndSelect(candidates, strategy, nil)
	if selected == nil {
		return "", fmt.Errorf("no available game server in pool for fleet %s", fleetName)
	}

	pool[selected.Name].Allocated = true
	pool[selected.Name].LastUsedAt = time.Now()
	pool[selected.Name].AllocationCount++
	return selected.Name, nil
}
