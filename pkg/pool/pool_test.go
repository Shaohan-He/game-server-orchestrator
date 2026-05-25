package pool

import (
	"testing"
	"time"

	"github.com/noneedtostudy/game-server-orchestrator/api/v1alpha1"
)

func TestRegisterAndAllocate(t *testing.T) {
	pm := NewPoolManager(nil)

	// Register servers in two different fleets.
	pm.Register("fleet-a", "server-1")
	pm.Register("fleet-a", "server-2")
	pm.Register("fleet-b", "server-3")

	// Fleet-a should have 2 available, fleet-b should have 1.
	name, err := pm.Allocate("fleet-a", "")
	if err != nil {
		t.Fatalf("allocate in fleet-a: %v", err)
	}
	if name == "" {
		t.Error("expected a server name")
	}

	// The allocated server should not be returned again.
	name2, err := pm.Allocate("fleet-a", "")
	if err != nil {
		t.Fatalf("second allocate in fleet-a: %v", err)
	}
	if name2 == name {
		t.Errorf("expected a different server, got same: %s", name2)
	}

	// Third allocate should fail — no more available in fleet-a.
	if _, err := pm.Allocate("fleet-a", ""); err == nil {
		t.Error("expected error when pool is exhausted")
	}

	// Fleet-b should still have its server.
	name3, err := pm.Allocate("fleet-b", "")
	if err != nil {
		t.Fatalf("allocate in fleet-b: %v", err)
	}
	if name3 != "server-3" {
		t.Errorf("expected server-3, got %s", name3)
	}
}

func TestRelease(t *testing.T) {
	pm := NewPoolManager(nil)
	pm.Register("fleet", "server-1")

	// Allocate it.
	name, err := pm.Allocate("fleet", "")
	if err != nil {
		t.Fatal(err)
	}

	// Should not be allocatable again.
	if _, err := pm.Allocate("fleet", ""); err == nil {
		t.Error("expected exhaustion after allocate")
	}

	// Release it back.
	pm.Release("fleet", name)

	// Now it should be allocatable again.
	name2, err := pm.Allocate("fleet", "")
	if err != nil {
		t.Fatalf("allocate after release: %v", err)
	}
	if name2 != name {
		t.Errorf("expected %s, got %s", name, name2)
	}
}

func TestMarkMethods(t *testing.T) {
	pm := NewPoolManager(nil)
	pm.Register("fleet", "server-1")
	pm.Register("fleet", "server-2")

	// Mark allocated directly.
	pm.MarkAllocated("fleet", "server-1")
	if _, err := pm.Allocate("fleet", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.Allocate("fleet", ""); err == nil {
		t.Error("expected exhaustion — server-1 should be marked allocated")
	}

	// Mark available again (simulating release after restart).
	pm.MarkAvailable("fleet", "server-1")
	name, err := pm.Allocate("fleet", "")
	if err != nil {
		t.Fatal(err)
	}
	if name == "" {
		t.Error("expected an available server")
	}

	// Mark unhealthy.
	pm.MarkUnhealthy("fleet", "server-1")
	if _, err := pm.Allocate("fleet", ""); err == nil {
		t.Error("expected no server available after marking all as unhealthy/allocated")
	}

	// Remove.
	pm.Register("fleet", "server-3")
	pm.Remove("fleet", "server-3")
	stats := pm.Stats("fleet")
	if stats.Total != 2 { // server-1 (unhealthy) + server-2 (allocated), server-3 removed
		t.Errorf("expected 2 total after remove, got %d", stats.Total)
	}
}

func TestIdleServers(t *testing.T) {
	pm := NewPoolManager(nil)
	pm.UpdateFleetConfig("fleet", FleetPoolConfig{
		BufferSize:  3,
		IdleTimeout: 100 * time.Millisecond,
	})

	pm.Register("fleet", "server-1")
	pm.Register("fleet", "server-2")

	// Both just registered — not idle yet.
	idle := pm.IdleServers("fleet")
	if len(idle) != 0 {
		t.Errorf("expected 0 idle, got %d", len(idle))
	}

	// Wait past the idle timeout.
	time.Sleep(150 * time.Millisecond)

	idle = pm.IdleServers("fleet")
	if len(idle) != 2 {
		t.Errorf("expected 2 idle, got %d", len(idle))
	}

	// Allocating one should remove it from idle.
	if _, err := pm.Allocate("fleet", ""); err != nil {
		t.Fatal(err)
	}
	idle = pm.IdleServers("fleet")
	if len(idle) != 1 {
		t.Errorf("expected 1 idle after allocation, got %d", len(idle))
	}
}

func TestStats(t *testing.T) {
	pm := NewPoolManager(nil)
	pm.Register("fleet", "s1")
	pm.Register("fleet", "s2")
	pm.Register("fleet", "s3")

	pm.MarkAllocated("fleet", "s1")
	pm.MarkUnhealthy("fleet", "s2")

	stats := pm.Stats("fleet")
	if stats.Total != 3 {
		t.Errorf("total: expected 3, got %d", stats.Total)
	}
	if stats.Healthy != 2 {
		t.Errorf("healthy: expected 2, got %d", stats.Healthy)
	}
	if stats.Unhealthy != 1 {
		t.Errorf("unhealthy: expected 1, got %d", stats.Unhealthy)
	}
	if stats.Allocated != 1 {
		t.Errorf("allocated: expected 1, got %d", stats.Allocated)
	}
	if stats.BufferAvailable != 1 { // s3 is healthy and unallocated
		t.Errorf("buffer: expected 1, got %d", stats.BufferAvailable)
	}
}

func TestNeedsRefillAndRefillCount(t *testing.T) {
	pm := NewPoolManager(nil)
	pm.UpdateFleetConfig("fleet", FleetPoolConfig{
		BufferSize:  2,
		IdleTimeout: 300 * time.Second,
	})

	// Empty pool — needs refill.
	if !pm.NeedsRefill("fleet") {
		t.Error("empty pool should need refill")
	}
	if c := pm.RefillCount("fleet"); c != 2 {
		t.Errorf("refill count: expected 2, got %d", c)
	}

	// Add one server — still needs 1 more.
	pm.Register("fleet", "server-1")
	if c := pm.RefillCount("fleet"); c != 1 {
		t.Errorf("refill count: expected 1, got %d", c)
	}

	// Add second server — buffer satisfied.
	pm.Register("fleet", "server-2")
	if pm.NeedsRefill("fleet") {
		t.Error("should not need refill with buffer full")
	}
	if c := pm.RefillCount("fleet"); c != 0 {
		t.Errorf("refill count: expected 0, got %d", c)
	}

	// Allocate both — needs 2 again.
	if _, err := pm.Allocate("fleet", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pm.Allocate("fleet", ""); err != nil {
		t.Fatal(err)
	}
	if !pm.NeedsRefill("fleet") {
		t.Error("should need refill after allocating all")
	}
	if c := pm.RefillCount("fleet"); c != 2 {
		t.Errorf("refill count: expected 2, got %d", c)
	}
}

func TestFleetIsolation(t *testing.T) {
	pm := NewPoolManager(nil)

	// Fleets with different buffer configurations.
	pm.UpdateFleetConfig("fleet-a", FleetPoolConfig{BufferSize: 5, IdleTimeout: 60 * time.Second})
	pm.UpdateFleetConfig("fleet-b", FleetPoolConfig{BufferSize: 1, IdleTimeout: 10 * time.Second})

	// Each fleet registers its own servers.
	pm.Register("fleet-a", "a-1")
	pm.Register("fleet-a", "a-2")
	pm.Register("fleet-b", "b-1")

	// Fleet-a stats.
	statsA := pm.Stats("fleet-a")
	if statsA.Total != 2 || statsA.BufferAvailable != 2 {
		t.Errorf("fleet-a stats: total=%d buf=%d", statsA.Total, statsA.BufferAvailable)
	}
	if !pm.NeedsRefill("fleet-a") {
		t.Error("fleet-a should need refill (buffer=5, have=2)")
	}

	// Fleet-b stats.
	statsB := pm.Stats("fleet-b")
	if statsB.Total != 1 || statsB.BufferAvailable != 1 {
		t.Errorf("fleet-b stats: total=%d buf=%d", statsB.Total, statsB.BufferAvailable)
	}
	if pm.NeedsRefill("fleet-b") {
		t.Error("fleet-b should not need refill (buffer=1, have=1)")
	}

	// Allocating from fleet-a doesn't affect fleet-b.
	name, _ := pm.Allocate("fleet-a", "")
	_ = name
	if pm.Stats("fleet-a").BufferAvailable != 1 {
		t.Error("fleet-a buffer should be down to 1")
	}
	if pm.Stats("fleet-b").BufferAvailable != 1 {
		t.Error("fleet-b should be unaffected")
	}
}

func TestUnknownFleet(t *testing.T) {
	pm := NewPoolManager(nil)

	if _, err := pm.Allocate("nonexistent", ""); err == nil {
		t.Error("expected error for nonexistent fleet")
	}

	idle := pm.IdleServers("nonexistent")
	if len(idle) != 0 {
		t.Error("expected no idle servers for nonexistent fleet")
	}

	stats := pm.Stats("nonexistent")
	if stats.Total != 0 {
		t.Error("expected zero stats for nonexistent fleet")
	}
}

func TestUpdateFleetConfig(t *testing.T) {
	pm := NewPoolManager(nil)
	pm.Register("fleet", "server-1")

	// Default config: no idle timeout set.
	idle := pm.IdleServers("fleet")
	if len(idle) != 0 {
		t.Error("expected no idle with zero timeout")
	}

	// Update config with a short timeout.
	pm.UpdateFleetConfig("fleet", FleetPoolConfig{
		BufferSize:  3,
		IdleTimeout: 50 * time.Millisecond,
	})

	time.Sleep(100 * time.Millisecond)

	idle = pm.IdleServers("fleet")
	if len(idle) != 1 {
		t.Errorf("expected 1 idle after config update, got %d", len(idle))
	}
}

func TestRoundRobinAllocation(t *testing.T) {
	pm := NewPoolManager(nil)

	// Register 3 servers in a fleet.
	pm.Register("fleet", "server-a")
	pm.Register("fleet", "server-b")
	pm.Register("fleet", "server-c")

	// Round-robin should cycle through servers in order.
	first, _ := pm.Allocate("fleet", v1alpha1.AllocationRoundRobin)
	pm.Release("fleet", first)

	second, _ := pm.Allocate("fleet", v1alpha1.AllocationRoundRobin)
	pm.Release("fleet", second)

	third, _ := pm.Allocate("fleet", v1alpha1.AllocationRoundRobin)
	pm.Release("fleet", third)

	// After releasing all, the next allocation should wrap back to the first server.
	fourth, _ := pm.Allocate("fleet", v1alpha1.AllocationRoundRobin)

	// Since we release after each allocation, the same 3 servers are always available.
	// Round-robin cycles: server-a → server-b → server-c → server-a.
	if first == second {
		t.Error("round-robin: first and second should be different")
	}
	if second == third {
		t.Error("round-robin: second and third should be different")
	}
	if third == first {
		t.Error("round-robin: third and first should be different")
	}
	if fourth != first {
		t.Errorf("round-robin: fourth should wrap back to first (%s), got %s", first, fourth)
	}
}

func TestRoundRobinVsDefault(t *testing.T) {
	pm := NewPoolManager(nil)

	// Register 3 servers.
	pm.Register("fleet", "srv-1")
	pm.Register("fleet", "srv-2")
	pm.Register("fleet", "srv-3")

	// Default strategy (empty) picks the first available alphabetically.
	def, _ := pm.Allocate("fleet", "")
	if def != "srv-1" {
		t.Errorf("default: expected srv-1 (first sorted), got %s", def)
	}
	pm.Release("fleet", def)

	// Round-robin cycles: sorted order is srv-1, srv-2, srv-3.
	rr1, _ := pm.Allocate("fleet", v1alpha1.AllocationRoundRobin)
	pm.Release("fleet", rr1)
	rr2, _ := pm.Allocate("fleet", v1alpha1.AllocationRoundRobin)
	pm.Release("fleet", rr2)
	rr3, _ := pm.Allocate("fleet", v1alpha1.AllocationRoundRobin)

	if rr1 != "srv-1" || rr2 != "srv-2" || rr3 != "srv-3" {
		t.Errorf("round-robin order: expected srv-1, srv-2, srv-3, got %s, %s, %s", rr1, rr2, rr3)
	}
}
