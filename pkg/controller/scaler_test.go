package controller

import (
	"context"
	"testing"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/health"
)

func makeFleet(replicas, allocated, draining, buffer, players int32, paused bool) *v1alpha1.GameServerFleet {
	return &v1alpha1.GameServerFleet{
		Spec: v1alpha1.GameServerFleetSpec{
			Paused: paused,
		},
		Status: v1alpha1.GameServerFleetStatus{
			Replicas:          replicas,
			AllocatedReplicas: allocated,
			DrainingReplicas:  draining,
			BufferPool:        buffer,
			TotalPlayers:      players,
		},
	}
}

func makePolicy(min, max, targetPlayers, bufferSize int32, nodeHealthEnabled bool) *v1alpha1.AutoscalerPolicy {
	return &v1alpha1.AutoscalerPolicy{
		Spec: v1alpha1.AutoscalerPolicySpec{
			MinReplicas: min,
			MaxReplicas: max,
			Buffer: v1alpha1.BufferConfig{
				Size:               bufferSize,
				IdleTimeoutSeconds: 300,
			},
			ScalingMetric: v1alpha1.ScalingMetricConfig{
				Type:        v1alpha1.ScalingMetricPlayersPerServer,
				TargetValue: targetPlayers,
			},
			NodeHealth: v1alpha1.NodeHealthConfig{
				Enabled: nodeHealthEnabled,
			},
		},
	}
}

func TestScalerPausedFleet(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(5, 0, 0, 5, 0, true)
	policy := makePolicy(1, 10, 80, 3, false)

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != ScaleFrozen {
		t.Errorf("paused fleet: expected FROZEN, got %s", result.Decision)
	}
}

func TestScalerScaleUp(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(3, 3, 0, 0, 200, false)
	policy := makePolicy(1, 20, 80, 3, false)
	// 200 players / 80 per server = 3 servers for players + 3 buffer = 6 desired
	// current = 3 → expected scale up by 3

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 200})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != ScaleUp {
		t.Errorf("expected SCALE_UP, got %s", result.Decision)
	}
	if result.DesiredReplicas != 6 {
		t.Errorf("expected 6 desired replicas, got %d", result.DesiredReplicas)
	}
	if result.Delta != 3 {
		t.Errorf("expected delta 3, got %d", result.Delta)
	}
}

func TestScalerScaleDown(t *testing.T) {
	s := NewScaler(nil)
	// 7 replicas, 0 players, buffer=7 (all warm). Need 0 for players + 2 buffer = 2.
	fleet := makeFleet(7, 0, 0, 7, 0, false)
	policy := makePolicy(1, 10, 80, 2, false)

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != ScaleDown {
		t.Errorf("expected SCALE_DOWN, got %s", result.Decision)
	}
	if result.DesiredReplicas != 2 {
		t.Errorf("expected 2 desired, got %d", result.DesiredReplicas)
	}
}

func TestScalerHoldWhenBufferTight(t *testing.T) {
	s := NewScaler(nil)
	// desired = 3 (players) + 2 (buffer) = 5, current = 5, buffer = 2 = bufferSize.
	fleet := makeFleet(5, 3, 0, 2, 240, false)
	policy := makePolicy(1, 10, 80, 2, false)

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 240})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != ScaleHold {
		t.Errorf("expected HOLD, got %s (reason: %s)", result.Decision, result.Reason)
	}
}

func TestScalerHoldWhenBufferLow(t *testing.T) {
	s := NewScaler(nil)
	// desired = ceil(50/80) = 1 + 2 = 3, current = 3, buffer = 0 (all allocated).
	// desired == current → HOLD
	fleet := makeFleet(3, 3, 0, 0, 50, false)
	policy := makePolicy(1, 10, 80, 2, false)

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 50})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != ScaleHold {
		t.Errorf("expected HOLD, got %s", result.Decision)
	}
}

func TestScalerClampToMin(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(0, 0, 0, 0, 0, false)
	policy := makePolicy(3, 10, 80, 2, false)
	// desired = 0 + 2 = 2, clamped to min=3.

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{})
	if err != nil {
		t.Fatal(err)
	}
	if result.DesiredReplicas != 3 {
		t.Errorf("expected min 3 replicas, got %d", result.DesiredReplicas)
	}
	if result.Decision != ScaleUp {
		t.Errorf("expected SCALE_UP to min, got %s", result.Decision)
	}
}

func TestScalerClampToMax(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(2, 2, 0, 0, 800, false)
	policy := makePolicy(1, 5, 80, 3, false)
	// desired = ceil(800/80) = 10 + 3 = 13, clamped to max=5.

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 800})
	if err != nil {
		t.Fatal(err)
	}
	if result.DesiredReplicas != 5 {
		t.Errorf("expected max 5, got %d", result.DesiredReplicas)
	}
}

func TestScalerNodeHealthProviderNil(t *testing.T) {
	// Node filter with nil provider — should not fail.
	nf := health.NewNodeFilter(nil)
	s := NewScaler(nf)
	fleet := makeFleet(3, 3, 0, 0, 200, false)
	policy := makePolicy(1, 20, 80, 3, true) // node health enabled

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 200})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != ScaleUp {
		t.Errorf("nil provider: expected SCALE_UP, got %s (reason: %s)", result.Decision, result.Reason)
	}
}

func TestScalerZeroPlayersPerServer(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(2, 0, 0, 2, 100, false)
	policy := makePolicy(1, 10, 0, 2, false) // playersPerServer = 0

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 100})
	if err != nil {
		t.Fatal(err)
	}
	// With 0 target, serversForPlayers = 0, desired = 0 + 2 = 2.
	if result.Decision != ScaleHold {
		t.Errorf("zero target: expected HOLD at 2, got %s (desired=%d)", result.Decision, result.DesiredReplicas)
	}
}

func TestScalerNoScaleDownWhenDraining(t *testing.T) {
	// Fleet has draining replicas — should not scale down more than buffer excess.
	s := NewScaler(nil)
	fleet := makeFleet(6, 2, 1, 3, 0, false) // 3 warm, 1 draining
	policy := makePolicy(1, 10, 80, 2, false)

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{})
	if err != nil {
		t.Fatal(err)
	}
	// desired = 0 + 2 = 2. current = 6. buffer = 3 > bufferSize = 2.
	// Should scale down because buffer is over-provisioned.
	if result.Decision != ScaleDown {
		t.Errorf("expected SCALE_DOWN (buffer=3 > target=2), got %s", result.Decision)
	}
}

func TestScalerBufferDefaultToOne(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(0, 0, 0, 0, 0, false)
	policy := makePolicy(1, 10, 80, 0, false) // buffer = 0, should default to 1

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{})
	if err != nil {
		t.Fatal(err)
	}
	if result.DesiredReplicas != 1 {
		t.Errorf("zero buffer: expected min 1, got %d", result.DesiredReplicas)
	}
}
