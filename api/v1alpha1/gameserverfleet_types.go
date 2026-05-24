package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=gsf
type GameServerFleet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GameServerFleetSpec   `json:"spec,omitempty"`
	Status GameServerFleetStatus `json:"status,omitempty"`
}

type GameServerFleetSpec struct {
	// Template is the Pod template for game servers in this fleet.
	Template GameServerTemplate `json:"template"`

	// AutoscalerRef references the AutoscalerPolicy for this fleet.
	AutoscalerRef corev1.LocalObjectReference `json:"autoscalerRef,omitempty"`

	// HealthCheck overrides the Pod-level health probe for game-server-specific checks.
	HealthCheck *corev1.Probe `json:"healthCheck,omitempty"`

	// SessionQuery specifies how to query active player sessions on a game server.
	SessionQuery *SessionQuery `json:"sessionQuery,omitempty"`

	// NodeSelector restricts game server scheduling to matching nodes.
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Paused suspends all scaling operations for this fleet.
	Paused bool `json:"paused,omitempty"`
}

type GameServerTemplate struct {
	// ObjectMeta overrides for generated Pods.
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is the Pod spec for game servers.
	Spec corev1.PodSpec `json:"spec,omitempty"`
}

type SessionQuery struct {
	// HTTPGet queries the game server's HTTP endpoint for active sessions.
	HTTPGet *corev1.HTTPGetAction `json:"httpGet,omitempty"`

	// PeriodSeconds is how often to query for session count during draining.
	PeriodSeconds int32 `json:"periodSeconds,omitempty"`
}

type GameServerFleetStatus struct {
	// Replicas is the total number of game server Pods in this fleet.
	Replicas int32 `json:"replicas"`

	// ReadyReplicas is the number of Pods that are Ready.
	ReadyReplicas int32 `json:"readyReplicas"`

	// AllocatedReplicas is the number of Pods currently assigned to matches.
	AllocatedReplicas int32 `json:"allocatedReplicas"`

	// DrainingReplicas is the number of Pods in the draining phase.
	DrainingReplicas int32 `json:"drainingReplicas"`

	// BufferPool is the number of warm, unallocated Pods available.
	BufferPool int32 `json:"bufferPool"`

	// TotalPlayers is the sum of active players across all allocated game servers.
	TotalPlayers int32 `json:"totalPlayers"`

	// Conditions represent the current state of the fleet.
	Conditions []FleetCondition `json:"conditions,omitempty"`
}

type FleetCondition struct {
	Type               FleetConditionType   `json:"type"`
	Status             metav1.ConditionStatus `json:"status"`
	LastTransitionTime metav1.Time           `json:"lastTransitionTime,omitempty"`
	Reason             string                `json:"reason,omitempty"`
	Message            string                `json:"message,omitempty"`
}

type FleetConditionType string

const (
	FleetAvailable   FleetConditionType = "Available"
	FleetScaling     FleetConditionType = "Scaling"
	FleetDegraded    FleetConditionType = "Degraded"
	FleetCircuitOpen FleetConditionType = "CircuitOpen"
)

// +kubebuilder:object:root=true
type GameServerFleetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GameServerFleet `json:"items"`
}
