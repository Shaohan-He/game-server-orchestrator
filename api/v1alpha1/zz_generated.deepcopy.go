package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto copies all properties into another object.
func (in *GameServerFleet) DeepCopyInto(out *GameServerFleet) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *GameServerFleet) DeepCopy() *GameServerFleet {
	if in == nil {
		return nil
	}
	out := new(GameServerFleet)
	in.DeepCopyInto(out)
	return out
}

func (in *GameServerFleet) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *GameServerFleetList) DeepCopyInto(out *GameServerFleetList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]GameServerFleet, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *GameServerFleetList) DeepCopy() *GameServerFleetList {
	if in == nil {
		return nil
	}
	out := new(GameServerFleetList)
	in.DeepCopyInto(out)
	return out
}

func (in *GameServerFleetList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *GameServerFleetSpec) DeepCopyInto(out *GameServerFleetSpec) {
	*out = *in
	in.Template.DeepCopyInto(&out.Template)
	out.AutoscalerRef = in.AutoscalerRef
	if in.HealthCheck != nil {
		in, out := &in.HealthCheck, &out.HealthCheck
		*out = new(corev1.Probe)
		(*in).DeepCopyInto(*out)
	}
	if in.SessionQuery != nil {
		in, out := &in.SessionQuery, &out.SessionQuery
		*out = new(SessionQuery)
		(*in).DeepCopyInto(*out)
	}
	if in.NodeSelector != nil {
		in, out := &in.NodeSelector, &out.NodeSelector
		*out = make(map[string]string, len(*in))
		for k, v := range *in {
			(*out)[k] = v
		}
	}
}

func (in *GameServerTemplate) DeepCopyInto(out *GameServerTemplate) {
	*out = *in
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

func (in *SessionQuery) DeepCopyInto(out *SessionQuery) {
	*out = *in
	if in.HTTPGet != nil {
		in, out := &in.HTTPGet, &out.HTTPGet
		*out = new(corev1.HTTPGetAction)
		(*in).DeepCopyInto(*out)
	}
}

func (in *GameServerFleetStatus) DeepCopyInto(out *GameServerFleetStatus) {
	*out = *in
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]FleetCondition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *FleetCondition) DeepCopyInto(out *FleetCondition) {
	*out = *in
	in.LastTransitionTime.DeepCopyInto(&out.LastTransitionTime)
}

// --- AutoscalerPolicy ---

func (in *AutoscalerPolicy) DeepCopyInto(out *AutoscalerPolicy) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
}

func (in *AutoscalerPolicy) DeepCopy() *AutoscalerPolicy {
	if in == nil {
		return nil
	}
	out := new(AutoscalerPolicy)
	in.DeepCopyInto(out)
	return out
}

func (in *AutoscalerPolicy) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *AutoscalerPolicyList) DeepCopyInto(out *AutoscalerPolicyList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]AutoscalerPolicy, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *AutoscalerPolicyList) DeepCopy() *AutoscalerPolicyList {
	if in == nil {
		return nil
	}
	out := new(AutoscalerPolicyList)
	in.DeepCopyInto(out)
	return out
}

func (in *AutoscalerPolicyList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *AutoscalerPolicySpec) DeepCopyInto(out *AutoscalerPolicySpec) {
	*out = *in
	out.Buffer = in.Buffer
	out.ScalingMetric = in.ScalingMetric
	if in.AuxiliaryMetrics != nil {
		in, out := &in.AuxiliaryMetrics, &out.AuxiliaryMetrics
		*out = make([]AuxiliaryMetricConfig, len(*in))
		copy(*out, *in)
	}
	out.Cooldown = in.Cooldown
	out.Drain = in.Drain
	out.Allocation = in.Allocation
	out.CircuitBreaker = in.CircuitBreaker
	out.NodeHealth = in.NodeHealth
}

// --- GameServerAllocation ---

func (in *GameServerAllocation) DeepCopyInto(out *GameServerAllocation) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *GameServerAllocation) DeepCopy() *GameServerAllocation {
	if in == nil {
		return nil
	}
	out := new(GameServerAllocation)
	in.DeepCopyInto(out)
	return out
}

func (in *GameServerAllocation) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *GameServerAllocationList) DeepCopyInto(out *GameServerAllocationList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]GameServerAllocation, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *GameServerAllocationList) DeepCopy() *GameServerAllocationList {
	if in == nil {
		return nil
	}
	out := new(GameServerAllocationList)
	in.DeepCopyInto(out)
	return out
}

func (in *GameServerAllocationList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *GameServerAllocationSpec) DeepCopyInto(out *GameServerAllocationSpec) {
	*out = *in
	out.FleetRef = in.FleetRef
	if in.Selectors != nil {
		in, out := &in.Selectors, &out.Selectors
		*out = new(metav1.LabelSelector)
		(*in).DeepCopyInto(*out)
	}
	in.Players.DeepCopyInto(&out.Players)
}

func (in *PlayerMeta) DeepCopyInto(out *PlayerMeta) {
	*out = *in
	if in.Regions != nil {
		in, out := &in.Regions, &out.Regions
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
}

func (in *GameServerAllocationStatus) DeepCopyInto(out *GameServerAllocationStatus) {
	*out = *in
	if in.GameServer != nil {
		in, out := &in.GameServer, &out.GameServer
		*out = new(AllocatedGameServer)
		**out = **in
	}
	if in.AllocatedAt != nil {
		in, out := &in.AllocatedAt, &out.AllocatedAt
		*out = (*in).DeepCopy()
	}
}
