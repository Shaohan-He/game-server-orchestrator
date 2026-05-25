package controller

import (
	"context"
	"fmt"

	"github.com/noneedtostudy/game-server-orchestrator/api/v1alpha1"
	"github.com/noneedtostudy/game-server-orchestrator/pkg/health"
)

type ScaleDecision string

const (
	ScaleUp     ScaleDecision = "SCALE_UP"
	ScaleDown   ScaleDecision = "SCALE_DOWN"
	ScaleHold   ScaleDecision = "HOLD"
	ScaleFrozen ScaleDecision = "FROZEN"
)

type ScaleResult struct {
	Decision       ScaleDecision
	CurrentReplicas int32
	DesiredReplicas int32
	Delta          int32
	Reason         string
	HealthyNodes   []string
	FilteredNodes  []string
}

type Scaler struct {
	nodeFilter *health.NodeFilter
}

func NewScaler(nf *health.NodeFilter) *Scaler {
	return &Scaler{nodeFilter: nf}
}

// Evaluate computes the desired replica count and returns a scaling decision.
func (s *Scaler) Evaluate(
	ctx context.Context,
	fleet *v1alpha1.GameServerFleet,
	policy *v1alpha1.AutoscalerPolicy,
	currentMetrics FleetMetrics,
) (*ScaleResult, error) {

	if fleet.Spec.Paused {
		return &ScaleResult{
			Decision:        ScaleFrozen,
			CurrentReplicas: fleet.Status.Replicas,
			DesiredReplicas: fleet.Status.Replicas,
			Reason:          "fleet is paused",
		}, nil
	}

	current := fleet.Status.Replicas
	allocated := fleet.Status.AllocatedReplicas
	draining := fleet.Status.DrainingReplicas
	buffer := fleet.Status.BufferPool

	// Primary metric: players per server → how many servers we need for current player load
	playersPerServer := policy.Spec.ScalingMetric.TargetValue
	var serversForPlayers int32
	if playersPerServer > 0 && currentMetrics.TotalPlayers > 0 {
		serversForPlayers = (currentMetrics.TotalPlayers + playersPerServer - 1) / playersPerServer
	}

	bufferSize := policy.Spec.Buffer.Size
	if bufferSize < 1 {
		bufferSize = 1
	}

	desired := serversForPlayers + bufferSize

	// Apply auxiliary metrics to adjust desired count based on resource pressure.
	var auxAdjustment int32
	for _, aux := range policy.Spec.AuxiliaryMetrics {
		if aux.Weight <= 0 {
			continue
		}
		switch aux.Type {
		case v1alpha1.ScalingMetricCPU:
			if currentMetrics.AverageCPU > 70 {
				cpuPressure := currentMetrics.AverageCPU / 100.0
				extra := int32(cpuPressure * float64(serversForPlayers) * aux.Weight)
				auxAdjustment += extra
			}
		case v1alpha1.ScalingMetricMemory:
			if currentMetrics.AverageMemoryMB > 0 {
				memPressure := currentMetrics.AverageMemoryMB / 4096.0 // per 4GB
				extra := int32(memPressure * float64(serversForPlayers) * aux.Weight)
				auxAdjustment += extra
			}
		}
	}
	desired += auxAdjustment

	// Clamp to min/max
	if desired < policy.Spec.MinReplicas {
		desired = policy.Spec.MinReplicas
	}
	if desired > policy.Spec.MaxReplicas {
		desired = policy.Spec.MaxReplicas
	}

	result := &ScaleResult{
		CurrentReplicas: current,
		DesiredReplicas: desired,
		Delta:           desired - current,
	}

	// Node health filtering for scale-up
	if desired > current && policy.Spec.NodeHealth.Enabled {
		healthy, unhealthy, err := s.nodeFilter.HealthyNodes(ctx, &policy.Spec.NodeHealth)
		if err != nil {
			// Degrade gracefully: allow scale-up but log the error
			result.Reason = fmt.Sprintf("node health check failed (%v), proceeding without filtering", err)
		} else {
			result.HealthyNodes = healthy
			result.FilteredNodes = unhealthy
			minHealthy := int32(float64(len(healthy)+len(unhealthy)) * policy.Spec.NodeHealth.MinHealthyNodeRatio)
			if int32(len(healthy)) < minHealthy {
				result.Decision = ScaleFrozen
				result.DesiredReplicas = current
				result.Delta = 0
				result.Reason = fmt.Sprintf("healthy node ratio below minimum (%d/%d healthy)",
					len(healthy), len(healthy)+len(unhealthy))
				return result, nil
			}
		}
	}

	switch {
	case desired > current:
		result.Decision = ScaleUp
		reason := fmt.Sprintf("player-driven scale-up: %d players, %d allocated, buffer %d (need %d servers)",
			currentMetrics.TotalPlayers, allocated, buffer, serversForPlayers)
		if auxAdjustment > 0 {
			reason += fmt.Sprintf(" +%d from aux metrics (cpu=%.0f%%, mem=%.0fMB)", auxAdjustment, currentMetrics.AverageCPU, currentMetrics.AverageMemoryMB)
		}
		result.Reason = reason
	case desired < current:
		// Only scale down if there are warm servers to drain
		if buffer > bufferSize {
			result.Decision = ScaleDown
			result.Reason = fmt.Sprintf("buffer over-provisioned: %d warm, target %d", buffer, bufferSize)
		} else {
			result.Decision = ScaleHold
			result.DesiredReplicas = current
			result.Delta = 0
			result.Reason = fmt.Sprintf("no excess buffer to drain (buffer %d, target %d, draining %d)",
				buffer, bufferSize, draining)
		}
	default:
		result.Decision = ScaleHold
		result.Reason = fmt.Sprintf("replicas at target: %d servers, %d players, buffer %d",
			current, currentMetrics.TotalPlayers, buffer)
	}

	return result, nil
}

// FleetMetrics holds the collected metrics for a fleet at reconciliation time.
type FleetMetrics struct {
	TotalPlayers    int32
	AverageCPU      float64
	AverageMemoryMB float64
	AllocationRate  float64
}
