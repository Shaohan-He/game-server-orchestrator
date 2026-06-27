package controller

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/metrics"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/pool"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// AllocateResult is the result of a successful game server allocation.
type AllocateResult struct {
	ServerName string
	Endpoint   string
	Node       string
}

// Allocator is the interface the REST API needs to perform allocations.
type Allocator interface {
	Allocate(ctx context.Context, fleetName, namespace string, strategy v1alpha1.AllocationStrategy, regions []string) (*AllocateResult, error)
	Release(ctx context.Context, fleetName, namespace, serverName string) error
}

// AllocationReconciler reconciles GameServerAllocation resources.
type AllocationReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	PoolMgr *pool.PoolManager
}

func (r *AllocationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()

	log.Printf("[INFO] [alloc-controller] reconciling %s/%s", req.Namespace, req.Name)

	var alloc v1alpha1.GameServerAllocation
	if err := r.Get(ctx, req.NamespacedName, &alloc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Only process Pending allocations.
	if alloc.Status.Phase != "" && alloc.Status.Phase != v1alpha1.AllocationPending {
		return ctrl.Result{}, nil
	}

	// Check TTL.
	if alloc.Spec.TTLSeconds > 0 {
		createdAt := alloc.CreationTimestamp.Time
		if time.Since(createdAt) > time.Duration(alloc.Spec.TTLSeconds)*time.Second {
			alloc.Status.Phase = v1alpha1.AllocationFailed
			alloc.Status.Message = "allocation TTL exceeded"
			if err := r.Status().Update(ctx, &alloc); err != nil {
				return ctrl.Result{}, fmt.Errorf("update failed status: %w", err)
			}
			metrics.RecordAllocation(alloc.Spec.FleetRef.Name, string(alloc.Spec.Strategy), "Failed", time.Since(start).Seconds())
			return ctrl.Result{}, nil
		}
	}

	fleetName := alloc.Spec.FleetRef.Name

	// Build allowed server set from selectors.
	var allowedNames map[string]bool
	if alloc.Spec.Selectors != nil && len(alloc.Spec.Selectors.MatchLabels) > 0 {
		var pods corev1.PodList
		selLabels := map[string]string{"director.gamefleet.io/fleet": fleetName}
		for k, v := range alloc.Spec.Selectors.MatchLabels {
			selLabels[k] = v
		}
		if err := r.List(ctx, &pods, client.InNamespace(req.Namespace), client.MatchingLabels(selLabels)); err != nil {
			log.Printf("[WARN] [alloc-controller] %s: list pods with selectors: %v", req.Name, err)
		} else {
			allowedNames = make(map[string]bool, len(pods.Items))
			for _, pod := range pods.Items {
				allowedNames[pod.Name] = true
			}
		}
	}

	// Determine strategy for allocation.
	strategy := alloc.Spec.Strategy
	if strategy == "" {
		strategy = v1alpha1.AllocationFewestPlayers
	}

	// Find an available game server from the fleet's pool.
	serverName, err := r.PoolMgr.AllocateWithFilter(fleetName, strategy, allowedNames)
	if err != nil {
		log.Printf("[INFO] [alloc-controller] %s: %v — requeueing", req.Name, err)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	// Get the allocated Pod to extract endpoint info.
	var pod corev1.Pod
	key := client.ObjectKey{Name: serverName, Namespace: req.Namespace}
	if err := r.Get(ctx, key, &pod); err != nil {
		r.PoolMgr.Release(fleetName, serverName)
		return ctrl.Result{}, fmt.Errorf("get pod %s: %w", serverName, err)
	}

	// Persist allocation to Pod label so state survives controller restart.
	if err := r.patchPodLabel(ctx, &pod, "allocated"); err != nil {
		r.PoolMgr.Release(fleetName, serverName)
		return ctrl.Result{}, fmt.Errorf("label pod %s: %w", serverName, err)
	}

	// Update allocation status.
	now := metav1.Now()
	alloc.Status.Phase = v1alpha1.AllocationAllocated
	alloc.Status.GameServer = &v1alpha1.AllocatedGameServer{
		Name:     serverName,
		Endpoint: fmt.Sprintf("%s:%d", pod.Status.PodIP, findGamePort(pod.Spec.Containers)),
		Node:     pod.Spec.NodeName,
	}
	alloc.Status.AllocatedAt = &now
	alloc.Status.SessionID = fmt.Sprintf("session-%d", time.Now().UnixNano())

	if err := r.Status().Update(ctx, &alloc); err != nil {
		r.PoolMgr.Release(fleetName, serverName)
		_ = r.patchPodLabel(ctx, &pod, "warm")
		return ctrl.Result{}, fmt.Errorf("update allocated status: %w", err)
	}

	metrics.RecordAllocation(alloc.Spec.FleetRef.Name, string(strategy), "Allocated", time.Since(start).Seconds())

	log.Printf("[OK] [alloc-controller] %s: allocated %s (%s) in %v", req.Name, serverName, alloc.Status.GameServer.Endpoint, time.Since(start))
	return ctrl.Result{}, nil
}

func (r *AllocationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.GameServerAllocation{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 10}).
		Complete(r)
}

// Allocate implements the Allocator interface for the REST API handler.
func (r *AllocationReconciler) Allocate(ctx context.Context, fleetName, namespace string, strategy v1alpha1.AllocationStrategy, regions []string) (*AllocateResult, error) {
	serverName, err := r.PoolMgr.AllocateWithFilter(fleetName, strategy, nil)
	if err != nil {
		return nil, fmt.Errorf("no available server: %w", err)
	}

	var pod corev1.Pod
	key := client.ObjectKey{Name: serverName, Namespace: namespace}
	if err := r.Get(ctx, key, &pod); err != nil {
		r.PoolMgr.Release(fleetName, serverName)
		return nil, fmt.Errorf("get pod %s: %w", serverName, err)
	}

	// Persist allocation to Pod label.
	if err := r.patchPodLabel(ctx, &pod, "allocated"); err != nil {
		r.PoolMgr.Release(fleetName, serverName)
		return nil, fmt.Errorf("label pod %s: %w", serverName, err)
	}

	return &AllocateResult{
		ServerName: serverName,
		Endpoint:   fmt.Sprintf("%s:%d", pod.Status.PodIP, findGamePort(pod.Spec.Containers)),
		Node:       pod.Spec.NodeName,
	}, nil
}

// Release implements the Allocator interface for the REST API handler.
func (r *AllocationReconciler) Release(ctx context.Context, fleetName, namespace, serverName string) error {
	var pod corev1.Pod
	key := client.ObjectKey{Name: serverName, Namespace: namespace}
	if err := r.Get(ctx, key, &pod); err == nil {
		_ = r.patchPodLabel(ctx, &pod, "warm")
	}

	r.PoolMgr.Release(fleetName, serverName)
	log.Printf("[INFO] [alloc-controller] %s/%s: released %s", namespace, fleetName, serverName)
	return nil
}

// patchPodLabel updates the director.gamefleet.io/phase label on a Pod.
func (r *AllocationReconciler) patchPodLabel(ctx context.Context, pod *corev1.Pod, phase string) error {
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels["director.gamefleet.io/phase"] = phase
	return r.Patch(ctx, pod, patch)
}

func findGamePort(containers []corev1.Container) int32 {
	for _, c := range containers {
		for _, p := range c.Ports {
			if p.ContainerPort > 0 && (p.Name == "game" || p.Protocol == corev1.ProtocolUDP) {
				return p.ContainerPort
			}
		}
	}
	if len(containers) > 0 && len(containers[0].Ports) > 0 {
		return containers[0].Ports[0].ContainerPort
	}
	return 7777
}
