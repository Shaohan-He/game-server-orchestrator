package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=asp
type AutoscalerPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AutoscalerPolicySpec `json:"spec,omitempty"`
}

type AutoscalerPolicySpec struct {
	// MinReplicas is the lower bound for fleet replica count.
	MinReplicas int32 `json:"minReplicas"`

	// MaxReplicas is the upper bound for fleet replica count.
	MaxReplicas int32 `json:"maxReplicas"`

	// Buffer configures the warm pool of pre-started game servers.
	Buffer BufferConfig `json:"buffer"`

	// ScalingMetric defines the primary metric for scaling decisions.
	ScalingMetric ScalingMetricConfig `json:"scalingMetric"`

	// AuxiliaryMetrics are weighted secondary metrics.
	AuxiliaryMetrics []AuxiliaryMetricConfig `json:"auxiliaryMetrics,omitempty"`

	// Cooldown prevents rapid back-and-forth scaling.
	Cooldown CooldownConfig `json:"cooldown"`

	// Drain configures the graceful drain protocol.
	Drain DrainConfig `json:"drain"`

	// Allocation configures how game servers are selected for matchmaker requests.
	Allocation AllocationConfig `json:"allocation"`

	// CircuitBreaker prevents cascading failures from bad metrics.
	CircuitBreaker CircuitBreakerConfig `json:"circuitBreaker,omitempty"`

	// NodeHealth configures node-health-aware scaling.
	NodeHealth NodeHealthConfig `json:"nodeHealth,omitempty"`
}

type BufferConfig struct {
	// Size is the target number of warm, unallocated game servers.
	Size int32 `json:"size"`

	// IdleTimeoutSeconds is how long a warm server can sit idle before being reclaimed.
	IdleTimeoutSeconds int32 `json:"idleTimeoutSeconds,omitempty"`
}

type ScalingMetricType string

const (
	ScalingMetricPlayersPerServer ScalingMetricType = "PlayersPerServer"
	ScalingMetricAllocationRate   ScalingMetricType = "AllocationRate"
	ScalingMetricCPU              ScalingMetricType = "CPU"
	ScalingMetricMemory           ScalingMetricType = "Memory"
)

type ScalingMetricConfig struct {
	// Type selects the scaling metric.
	Type ScalingMetricType `json:"type"`

	// TargetValue is the desired value for this metric per server.
	TargetValue int32 `json:"targetValue"`
}

type AuxiliaryMetricConfig struct {
	// Type selects the auxiliary metric.
	Type ScalingMetricType `json:"type"`

	// Weight is the relative importance (0.0 - 1.0).
	Weight float64 `json:"weight"`
}

type CooldownConfig struct {
	// ScaleUpSeconds is the minimum interval between consecutive scale-up operations.
	ScaleUpSeconds int32 `json:"scaleUpSeconds"`

	// ScaleDownSeconds is the minimum interval between consecutive scale-down operations.
	ScaleDownSeconds int32 `json:"scaleDownSeconds"`
}

type DrainConfig struct {
	// TimeoutSeconds is the max wait time for active sessions to end.
	TimeoutSeconds int32 `json:"timeoutSeconds"`

	// IntervalSeconds is the polling interval for session count checks.
	IntervalSeconds int32 `json:"intervalSeconds"`

	// ForceAfterSeconds is the hard deadline after which the server is killed regardless.
	ForceAfterSeconds int32 `json:"forceAfterSeconds,omitempty"`
}

type AllocationStrategy string

const (
	AllocationFewestPlayers AllocationStrategy = "FewestPlayers"
	AllocationLowestLatency AllocationStrategy = "LowestLatency"
	AllocationRoundRobin    AllocationStrategy = "RoundRobin"
	AllocationStrictBinPack AllocationStrategy = "StrictBinPack"
)

type AllocationConfig struct {
	// Strategy selects how game servers are picked for allocation.
	Strategy AllocationStrategy `json:"strategy"`
}

type CircuitBreakerConfig struct {
	// ConsecutiveFailures is the number of failed reconciles before opening the circuit.
	ConsecutiveFailures int32 `json:"consecutiveFailures"`

	// CooldownPeriodSeconds is how long the circuit stays open before attempting a half-open probe.
	CooldownPeriodSeconds int32 `json:"cooldownPeriodSeconds"`
}

type NodeHealthProvider string

const (
	NodeHealthProviderNHW    NodeHealthProvider = "nhw"
	NodeHealthProviderStatic NodeHealthProvider = "static"
)

type NodeHealthConfig struct {
	// Enabled toggles node-health-aware scaling.
	Enabled bool `json:"enabled"`

	// Provider selects the health data source.
	Provider NodeHealthProvider `json:"provider,omitempty"`

	// NHWEndpoint is the Node Health Watcher API base URL.
	NHWEndpoint string `json:"nhwEndpoint,omitempty"`

	// MinHealthyNodeRatio is the minimum fraction of nodes that must be healthy to allow scale-up.
	MinHealthyNodeRatio float64 `json:"minHealthyNodeRatio,omitempty"`
}

// +kubebuilder:object:root=true
type AutoscalerPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AutoscalerPolicy `json:"items"`
}
