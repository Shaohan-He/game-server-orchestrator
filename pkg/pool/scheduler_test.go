package pool

import (
	"testing"

	"github.com/noneedtostudy/game-server-orchestrator/api/v1alpha1"
)

func makeCandidates() []GameServerInfo {
	return []GameServerInfo{
		{Name: "srv-a", PlayerCount: 10, LatencyMs: 50, Allocated: false},
		{Name: "srv-b", PlayerCount: 0, LatencyMs: 100, Allocated: false},
		{Name: "srv-c", PlayerCount: 3, LatencyMs: 20, Allocated: false},
		{Name: "srv-d", PlayerCount: 8, LatencyMs: 80, Allocated: true},
	}
}

func TestFewestPlayers(t *testing.T) {
	result := RankAndSelect(makeCandidates(), v1alpha1.AllocationFewestPlayers, nil)
	if result == nil {
		t.Fatal("expected a result")
	}
	if result.Name != "srv-b" {
		t.Errorf("fewest players: expected srv-b (0 players), got %s", result.Name)
	}
}

func TestLowestLatency(t *testing.T) {
	result := RankAndSelect(makeCandidates(), v1alpha1.AllocationLowestLatency, nil)
	if result == nil {
		t.Fatal("expected a result")
	}
	if result.Name != "srv-c" {
		t.Errorf("lowest latency: expected srv-c (20ms), got %s", result.Name)
	}
}

func TestStrictBinPack(t *testing.T) {
	result := RankAndSelect(makeCandidates(), v1alpha1.AllocationStrictBinPack, nil)
	if result == nil {
		t.Fatal("expected a result")
	}
	if result.Name != "srv-a" {
		t.Errorf("strict bin pack: expected srv-a (10 players), got %s", result.Name)
	}
}

func TestRoundRobin(t *testing.T) {
	result := RankAndSelect(makeCandidates(), v1alpha1.AllocationRoundRobin, nil)
	if result == nil {
		t.Fatal("expected a result")
	}
	if result.Allocated {
		t.Error("round robin should not return an allocated server")
	}
}

func TestAllAllocatedReturnsNil(t *testing.T) {
	candidates := []GameServerInfo{
		{Name: "srv-a", Allocated: true},
		{Name: "srv-b", Allocated: true},
	}
	result := RankAndSelect(candidates, v1alpha1.AllocationFewestPlayers, nil)
	if result != nil {
		t.Error("expected nil when all servers are allocated")
	}
}

func TestEmptyCandidatesReturnsNil(t *testing.T) {
	result := RankAndSelect(nil, v1alpha1.AllocationFewestPlayers, nil)
	if result != nil {
		t.Error("expected nil for empty candidates")
	}
}

func TestSelectForDrain(t *testing.T) {
	candidates := []GameServerInfo{
		{Name: "srv-a", PlayerCount: 50},
		{Name: "srv-b", PlayerCount: 0},
		{Name: "srv-c", PlayerCount: 10},
		{Name: "srv-d", PlayerCount: 3},
	}

	selected := SelectForDrain(candidates, 2)
	if len(selected) != 2 {
		t.Fatalf("expected 2, got %d", len(selected))
	}
	if selected[0].Name != "srv-b" || selected[1].Name != "srv-d" {
		t.Errorf("expected srv-b and srv-d (fewest players), got %s, %s", selected[0].Name, selected[1].Name)
	}
}

func TestSelectForDrainMoreThanAvailable(t *testing.T) {
	candidates := []GameServerInfo{
		{Name: "srv-a", PlayerCount: 10},
		{Name: "srv-b", PlayerCount: 20},
	}

	selected := SelectForDrain(candidates, 5)
	if len(selected) != 2 {
		t.Errorf("expected 2 (capped), got %d", len(selected))
	}
}

func TestSelectForDrainEmpty(t *testing.T) {
	selected := SelectForDrain(nil, 3)
	if selected != nil {
		t.Error("expected nil for empty candidates")
	}
}

func TestDefaultStrategy(t *testing.T) {
	result := RankAndSelect(makeCandidates(), "UnknownStrategy", nil)
	if result == nil {
		t.Fatal("expected a result")
	}
	if result.Name != "srv-b" {
		t.Errorf("default strategy: expected srv-b (fewest players), got %s", result.Name)
	}
}
