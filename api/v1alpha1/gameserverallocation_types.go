package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=gsa
type GameServerAllocation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GameServerAllocationSpec   `json:"spec,omitempty"`
	Status GameServerAllocationStatus `json:"status,omitempty"`
}

type GameServerAllocationSpec struct {
	// FleetRef identifies the target fleet.
	FleetRef FleetReference `json:"fleetRef"`

	// Selectors further constrain which game servers are eligible.
	Selectors *metav1.LabelSelector `json:"selectors,omitempty"`

	// Strategy overrides the fleet's default allocation strategy.
	Strategy AllocationStrategy `json:"strategy,omitempty"`

	// Players provides metadata for latency-optimized allocation.
	Players PlayerMeta `json:"players,omitempty"`

	// TTLSeconds is the maximum time to wait for a successful allocation.
	TTLSeconds int32 `json:"ttlSeconds,omitempty"`
}

type FleetReference struct {
	// Name of the GameServerFleet resource.
	Name string `json:"name"`
}

type PlayerMeta struct {
	// Regions is the list of cloud regions players are connecting from.
	Regions []string `json:"regions,omitempty"`

	// PreferredLatencyMs is the target maximum latency in milliseconds.
	PreferredLatencyMs int32 `json:"preferredLatencyMs,omitempty"`
}

type AllocationPhase string

const (
	AllocationPending   AllocationPhase = "Pending"
	AllocationAllocated AllocationPhase = "Allocated"
	AllocationFailed    AllocationPhase = "Failed"
	AllocationReleased  AllocationPhase = "Released"
)

type GameServerAllocationStatus struct {
	// Phase is the current state of this allocation request.
	Phase AllocationPhase `json:"phase"`

	// GameServer identifies the allocated game server.
	GameServer *AllocatedGameServer `json:"gameServer,omitempty"`

	// PlayerCount is the number of players on the allocated server at allocation time.
	PlayerCount int32 `json:"playerCount,omitempty"`

	// AllocatedAt is the timestamp when the allocation succeeded.
	AllocatedAt *metav1.Time `json:"allocatedAt,omitempty"`

	// SessionID is the matchmaker-assigned session identifier.
	SessionID string `json:"sessionId,omitempty"`

	// Message provides human-readable context for the current phase.
	Message string `json:"message,omitempty"`
}

type AllocatedGameServer struct {
	// Name of the game server Pod.
	Name string `json:"name"`

	// Endpoint is the address:port for player connections.
	Endpoint string `json:"endpoint"`

	// Node is the K8s node hosting this game server.
	Node string `json:"node"`
}

// +kubebuilder:object:root=true
type GameServerAllocationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GameServerAllocation `json:"items"`
}
