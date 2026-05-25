package drainer

import (
	"context"
	"testing"
	"time"

	"github.com/noneedtostudy/game-server-orchestrator/api/v1alpha1"
)

type mockSessionQuerier struct {
	sessions int32
	err      error
}

func (m *mockSessionQuerier) ActiveSessions(ctx context.Context, endpoint string) (int32, error) {
	return m.sessions, m.err
}

func testDrainConfig(timeout, interval, force int32) v1alpha1.DrainConfig {
	return v1alpha1.DrainConfig{
		TimeoutSeconds:  timeout,
		IntervalSeconds: interval,
		ForceAfterSeconds: force,
	}
}

func TestDrainCordonToDrain(t *testing.T) {
	d := New(testDrainConfig(600, 30, 1800), &mockSessionQuerier{sessions: 0})

	ds := &ServerState{
		Name:      "server-1",
		Endpoint:  "10.0.0.1:8080",
		Phase:     PhaseCordon,
		EnteredAt: time.Now(),
	}

	ready, err := d.Advance(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Error("cordon should not report ready")
	}
	if ds.Phase != PhaseDrain {
		t.Errorf("expected DRAIN after cordon, got %s", ds.Phase)
	}
}

func TestDrainWithActiveSessions(t *testing.T) {
	d := New(testDrainConfig(600, 0, 3600), &mockSessionQuerier{sessions: 5})

	ds := &ServerState{
		Name:           "server-1",
		Endpoint:       "10.0.0.1:8080",
		Phase:          PhaseDrain,
		ActiveSessions: 5,
		EnteredAt:      time.Now(),
		LastCheckedAt:  time.Now(),
		ForcedDeadline: time.Now().Add(1 * time.Hour),
	}

	ready, err := d.Advance(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Error("should not be ready with active sessions")
	}
	if ds.ActiveSessions != 5 {
		t.Errorf("expected 5 sessions, got %d", ds.ActiveSessions)
	}
}

func TestDrainZeroSessionsBecomesDecommission(t *testing.T) {
	d := New(testDrainConfig(0, 0, 3600), &mockSessionQuerier{sessions: 0})

	ds := &ServerState{
		Name:           "server-1",
		Endpoint:       "10.0.0.1:8080",
		Phase:          PhaseDrain,
		ActiveSessions: 0,
		EnteredAt:      time.Now().Add(-10 * time.Minute),
		LastCheckedAt:  time.Now(),
		ForcedDeadline: time.Now().Add(1 * time.Hour),
	}

	ready, err := d.Advance(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Error("should be ready for decommission with 0 sessions")
	}
	if ds.Phase != PhaseDecommission {
		t.Errorf("expected DECOMMISSION, got %s", ds.Phase)
	}
}

func TestDrainForceDeadline(t *testing.T) {
	d := New(testDrainConfig(600, 0, 1), &mockSessionQuerier{sessions: 100})

	ds := &ServerState{
		Name:           "server-1",
		Endpoint:       "10.0.0.1:8080",
		Phase:          PhaseDrain,
		ActiveSessions: 100,
		EnteredAt:      time.Now().Add(-2 * time.Second),
		LastCheckedAt:  time.Now(),
		ForcedDeadline: time.Now().Add(-1 * time.Second), // past due
	}

	ready, err := d.Advance(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Error("should be ready after force deadline")
	}
	if ds.Phase != PhaseForced {
		t.Errorf("expected FORCED, got %s", ds.Phase)
	}
}

func TestDecommissionTransitionsToDone(t *testing.T) {
	d := New(testDrainConfig(600, 30, 1800), &mockSessionQuerier{})

	ds := &ServerState{
		Name:     "server-1",
		Endpoint: "10.0.0.1:8080",
		Phase:    PhaseDecommission,
	}

	ready, err := d.Advance(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Error("decommission should report ready")
	}
	if ds.Phase != PhaseDone {
		t.Errorf("expected DONE, got %s", ds.Phase)
	}
}

func TestForcedTransitionsToDone(t *testing.T) {
	d := New(testDrainConfig(600, 30, 1800), &mockSessionQuerier{})

	ds := &ServerState{
		Name:     "server-1",
		Endpoint: "10.0.0.1:8080",
		Phase:    PhaseForced,
	}

	ready, err := d.Advance(context.Background(), ds)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Error("forced should report ready")
	}
	if ds.Phase != PhaseDone {
		t.Errorf("expected DONE, got %s", ds.Phase)
	}
}

func TestShouldCheck(t *testing.T) {
	d := New(testDrainConfig(600, 30, 1800), &mockSessionQuerier{})

	// Cordon phase: ShouldCheck returns false.
	ds := &ServerState{Phase: PhaseCordon, LastCheckedAt: time.Now()}
	if d.ShouldCheck(ds) {
		t.Error("cordon should not need checking")
	}

	// Drain phase, just checked: false.
	ds = &ServerState{Phase: PhaseDrain, LastCheckedAt: time.Now()}
	if d.ShouldCheck(ds) {
		t.Error("should not need check immediately after last check")
	}

	// Drain phase, checked long ago: true.
	ds = &ServerState{Phase: PhaseDrain, LastCheckedAt: time.Now().Add(-1 * time.Minute)}
	if !d.ShouldCheck(ds) {
		t.Error("should need check after interval elapsed")
	}

	// Done phase: false.
	ds = &ServerState{Phase: PhaseDone, LastCheckedAt: time.Now().Add(-1 * time.Hour)}
	if d.ShouldCheck(ds) {
		t.Error("done phase should not need checking")
	}
}

func TestStartDrain(t *testing.T) {
	d := New(testDrainConfig(600, 30, 1800), &mockSessionQuerier{})

	ds := d.StartDrain("server-1", "10.0.0.1:8080")
	if ds.Name != "server-1" {
		t.Errorf("expected server-1, got %s", ds.Name)
	}
	if ds.Endpoint != "10.0.0.1:8080" {
		t.Errorf("expected endpoint 10.0.0.1:8080, got %s", ds.Endpoint)
	}
	if ds.Phase != PhaseCordon {
		t.Errorf("expected CORDON, got %s", ds.Phase)
	}
	if ds.ForcedDeadline.Before(time.Now()) {
		t.Error("forced deadline should be in the future")
	}
}

func TestFullDrainLifecycle(t *testing.T) {
	d := New(testDrainConfig(0, 0, 3600), &mockSessionQuerier{sessions: 0})

	// Start drain.
	ds := d.StartDrain("server-1", "10.0.0.1:8080")

	// Step 1: Cordon → Drain.
	ready, _ := d.Advance(context.Background(), ds)
	if ready || ds.Phase != PhaseDrain {
		t.Fatalf("step 1 failed: ready=%v phase=%s", ready, ds.Phase)
	}

	// Step 2: Drain → Decommission (0 sessions, 0 timeout).
	ds.EnteredAt = time.Now().Add(-10 * time.Minute)
	ready, _ = d.Advance(context.Background(), ds)
	if !ready || ds.Phase != PhaseDecommission {
		t.Fatalf("step 2 failed: ready=%v phase=%s", ready, ds.Phase)
	}

	// Step 3: Decommission → Done.
	ready, _ = d.Advance(context.Background(), ds)
	if !ready || ds.Phase != PhaseDone {
		t.Fatalf("step 3 failed: ready=%v phase=%s", ready, ds.Phase)
	}
}
