package controller

import (
	"context"
	"testing"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"
)

func makePolicyWithAux(min, max, targetPlayers, bufferSize int32, auxMetrics []v1alpha1.AuxiliaryMetricConfig) *v1alpha1.AutoscalerPolicy {
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
			AuxiliaryMetrics: auxMetrics,
		},
	}
}

func TestScalerAuxCPUHighLoad(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(0, 0, 0, 0, 400, false)
	policy := makePolicyWithAux(1, 30, 80, 2, []v1alpha1.AuxiliaryMetricConfig{
		{Type: v1alpha1.ScalingMetricCPU, Weight: 1.0},
	})

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 400, AverageCPU: 95})
	if err != nil {
		t.Fatal(err)
	}
	if result.DesiredReplicas < 7 {
		t.Errorf("aux CPU should increase desired beyond base 7; got %d", result.DesiredReplicas)
	}
}

func TestScalerAuxCPUNoEffectWhenLow(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(3, 1, 0, 2, 160, false)
	policy := makePolicyWithAux(1, 20, 80, 3, []v1alpha1.AuxiliaryMetricConfig{
		{Type: v1alpha1.ScalingMetricCPU, Weight: 1.0},
	})

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 160, AverageCPU: 50})
	if err != nil {
		t.Fatal(err)
	}
	if result.DesiredReplicas != 5 {
		t.Errorf("low CPU: expected 5, got %d", result.DesiredReplicas)
	}
}

func TestScalerAuxMemoryEffect(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(0, 0, 0, 0, 160, false)
	policy := makePolicyWithAux(1, 20, 80, 2, []v1alpha1.AuxiliaryMetricConfig{
		{Type: v1alpha1.ScalingMetricMemory, Weight: 1.0},
	})

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 160, AverageMemoryMB: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if result.DesiredReplicas < 4 {
		t.Errorf("aux memory should increase; got %d", result.DesiredReplicas)
	}
}

func TestScalerAuxZeroWeightNoEffect(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(0, 0, 0, 0, 160, false)
	policy := makePolicyWithAux(1, 20, 80, 2, []v1alpha1.AuxiliaryMetricConfig{
		{Type: v1alpha1.ScalingMetricCPU, Weight: 0},
	})

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 160, AverageCPU: 95})
	if err != nil {
		t.Fatal(err)
	}
	if result.DesiredReplicas != 4 {
		t.Errorf("zero weight: expected 4, got %d", result.DesiredReplicas)
	}
}

func TestScalerAuxMultipleMetrics(t *testing.T) {
	s := NewScaler(nil)
	fleet := makeFleet(0, 0, 0, 0, 160, false)
	policy := makePolicyWithAux(1, 30, 80, 2, []v1alpha1.AuxiliaryMetricConfig{
		{Type: v1alpha1.ScalingMetricCPU, Weight: 0.5},
		{Type: v1alpha1.ScalingMetricMemory, Weight: 0.5},
	})

	result, err := s.Evaluate(context.Background(), fleet, policy, FleetMetrics{TotalPlayers: 160, AverageCPU: 90, AverageMemoryMB: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if result.DesiredReplicas < 5 {
		t.Errorf("multiple aux: expected >= 5, got %d", result.DesiredReplicas)
	}
}
