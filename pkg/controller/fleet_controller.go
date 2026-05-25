package controller

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/noneedtostudy/game-server-orchestrator/api/v1alpha1"
	"github.com/noneedtostudy/game-server-orchestrator/pkg/drainer"
	"github.com/noneedtostudy/game-server-orchestrator/pkg/metrics"
	"github.com/noneedtostudy/game-server-orchestrator/pkg/notifier"
	"github.com/noneedtostudy/game-server-orchestrator/pkg/pool"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// FleetReconciler reconciles GameServerFleet resources.
type FleetReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Scaler    *Scaler
	Drainer   *drainer.Drainer
	PoolMgr   *pool.PoolManager
	Notifier  *notifier.Notifier
	Scraper   *metrics.Scraper

	// Circuit breaker state per fleet.
	cbMu                sync.RWMutex
	consecutiveFailures map[string]int
	circuitOpenUntil    map[string]time.Time

	// drainStates tracks in-progress drains so the state machine survives
	// across 15-second reconcile cycles.
	drainStates map[string]*drainer.ServerState
	drainMu     sync.Mutex

	lastScaleUpAt   map[string]time.Time
	lastScaleDownAt map[string]time.Time
	cooldownMu      sync.Mutex
}

// fleetStatusSnapshot holds computed fleet status values from metrics collection.
type fleetStatusSnapshot struct {
	replicas          int32
	allocatedReplicas int32
	drainingReplicas  int32
	readyReplicas     int32
	bufferPool        int32
	totalPlayers      int32
}

func (r *FleetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	defer func() {
		metrics.ReconcileDuration.WithLabelValues("fleet").Observe(time.Since(start).Seconds())
	}()

	log.Printf("[INFO] [fleet-controller] reconciling %s/%s", req.Namespace, req.Name)

	var fleet v1alpha1.GameServerFleet
	if err := r.Get(ctx, req.NamespacedName, &fleet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if r.isCircuitOpen(req.Name) {
		log.Printf("[WARN] [fleet-controller] %s: circuit breaker open, skipping reconcile", req.Name)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	policy, err := r.getPolicy(ctx, &fleet)
	if err != nil {
		r.recordFailure(req.Name)
		return ctrl.Result{}, fmt.Errorf("get policy for %s: %w", req.Name, err)
	}

	// Keep per-fleet pool config up-to-date so idle timeout, buffer size etc. are correct.
	r.PoolMgr.UpdateFleetConfig(req.Name, pool.FleetPoolConfig{
		BufferSize:  policy.Spec.Buffer.Size,
		IdleTimeout: time.Duration(policy.Spec.Buffer.IdleTimeoutSeconds) * time.Second,
	})

	dryRun := fleet.Annotations["director.gamefleet.io/dry-run"] == "true"

	// Rebuild pool state from Pod labels so it survives controller restarts.
	if err := r.syncPoolFromLabels(ctx, &fleet); err != nil {
		log.Printf("[WARN] [fleet-controller] %s: pool sync: %v", req.Name, err)
	}

	statusSnapshot, fm, err := r.collectFleetMetrics(ctx, &fleet)
	if err != nil {
		log.Printf("[WARN] [fleet-controller] %s: metrics collection: %v", req.Name, err)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, fmt.Errorf("metrics collection failed for %s: %w", req.Name, err)
	}

	// Apply computed status snapshot to the fleet object.
	r.applyStatusSnapshot(&fleet, statusSnapshot)

	result, err := r.Scaler.Evaluate(ctx, &fleet, policy, fm)
	if err != nil {
		r.recordFailure(req.Name)
		return ctrl.Result{}, fmt.Errorf("scale evaluate: %w", err)
	}

	if result.Decision == ScaleUp {
		if wait := r.cooldownRemaining(req.Name, &policy.Spec.Cooldown, true); wait > 0 {
			result.Decision = ScaleHold
			result.Reason = fmt.Sprintf("cooldown: %s remaining before next scale-up", wait)
		}
	}
	if result.Decision == ScaleDown {
		if wait := r.cooldownRemaining(req.Name, &policy.Spec.Cooldown, false); wait > 0 {
			result.Decision = ScaleHold
			result.Reason = fmt.Sprintf("cooldown: %s remaining before next scale-down", wait)
		}
	}

	switch result.Decision {
	case ScaleUp:
		if err := r.executeScaleUp(ctx, &fleet, result, dryRun); err != nil {
			r.recordFailure(req.Name)
			return ctrl.Result{}, fmt.Errorf("scale up: %w", err)
		}
		metrics.RecordScaleUp(req.Name, req.Namespace)
		r.resetCircuit(req.Name)

	case ScaleDown:
		if err := r.executeScaleDown(ctx, &fleet, result, policy, dryRun); err != nil {
			r.recordFailure(req.Name)
			return ctrl.Result{}, fmt.Errorf("scale down: %w", err)
		}
		metrics.RecordScaleDown(req.Name, req.Namespace)
		r.resetCircuit(req.Name)

	case ScaleFrozen:
		log.Printf("[WARN] [fleet-controller] %s: scaling frozen — %s", req.Name, result.Reason)

	case ScaleHold:
		log.Printf("[INFO] [fleet-controller] %s: holding — %s", req.Name, result.Reason)
		r.resetCircuit(req.Name)
	}

	// Advance the drain state machine — this is where Pods actually get deleted.
	r.advanceDraining(ctx, &fleet)

	if err := r.updateStatus(ctx, &fleet, result); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	if result.Decision != ScaleHold && r.Notifier != nil {
		r.Notifier.NotifyScalingEvent(ctx, notifier.ScalingEvent{
			Timestamp:        time.Now(),
			Fleet:            req.Name,
			Namespace:        req.Namespace,
			Decision:         string(result.Decision),
			CurrentReplicas:  result.CurrentReplicas,
			DesiredReplicas:  result.DesiredReplicas,
			Reason:           result.Reason,
			Nodes:            result.HealthyNodes,
		})
	}

	metrics.SetFleetMetrics(req.Name, req.Namespace, fleet.Status.Replicas, fleet.Status.TotalPlayers, fleet.Status.BufferPool)

	log.Printf("[INFO] [fleet-controller] %s: reconcile complete (%s, %v)", req.Name, result.Decision, time.Since(start))
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func (r *FleetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.GameServerFleet{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 5}).
		Complete(r)
}

func (r *FleetReconciler) getPolicy(ctx context.Context, fleet *v1alpha1.GameServerFleet) (*v1alpha1.AutoscalerPolicy, error) {
	if fleet.Spec.AutoscalerRef.Name == "" {
		return nil, fmt.Errorf("no autoscalerRef on fleet %s", fleet.Name)
	}

	var policy v1alpha1.AutoscalerPolicy
	key := types.NamespacedName{
		Name:      fleet.Spec.AutoscalerRef.Name,
		Namespace: fleet.Namespace,
	}
	if err := r.Get(ctx, key, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// syncPoolFromLabels rebuilds the in-memory pool state and drain states from Pod labels
// so state survives controller restarts.
func (r *FleetReconciler) syncPoolFromLabels(ctx context.Context, fleet *v1alpha1.GameServerFleet) error {
	var pods corev1.PodList
	labels := map[string]string{
		"director.gamefleet.io/fleet": fleet.Name,
	}
	if err := r.List(ctx, &pods, client.InNamespace(fleet.Namespace), client.MatchingLabels(labels)); err != nil {
		return err
	}

	r.drainMu.Lock()
	if r.drainStates == nil {
		r.drainStates = make(map[string]*drainer.ServerState)
	}
	r.drainMu.Unlock()

	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		phase := pod.Labels["director.gamefleet.io/phase"]
		endpoint := r.metricsEndpoint(pod.Status.PodIP, fleet)
		if pod.Status.PodIP == "" {
			endpoint = pod.Name + fmt.Sprintf(":%d", findMetricsPort(fleet.Spec.Template.Spec))
		}

		r.PoolMgr.Register(fleet.Name, pod.Name)
		switch phase {
		case "allocated":
			r.PoolMgr.MarkAllocated(fleet.Name, pod.Name)
		case "draining":
			r.PoolMgr.MarkUnhealthy(fleet.Name, pod.Name)
			// Rebuild drain state so draining Pods are not stuck forever.
			r.drainMu.Lock()
			if _, exists := r.drainStates[pod.Name]; !exists {
				ds := &drainer.ServerState{
					Name:           pod.Name,
					Endpoint:       endpoint,
					Phase:          drainer.PhaseDrain,
					ActiveSessions: 0,
					EnteredAt:      time.Now(),
					LastCheckedAt:  time.Now(),
				}
				r.drainStates[pod.Name] = ds
				log.Printf("[INFO] [fleet-controller] %s: recovered drain state for %s", fleet.Name, pod.Name)
			}
			r.drainMu.Unlock()
		default:
			// Warm or unlabeled — ensure available to the allocator.
			r.PoolMgr.MarkAvailable(fleet.Name, pod.Name)
		}
	}
	return nil
}

func (r *FleetReconciler) collectFleetMetrics(ctx context.Context, fleet *v1alpha1.GameServerFleet) (fleetStatusSnapshot, FleetMetrics, error) {
	var snapshot fleetStatusSnapshot

	var pods corev1.PodList
	labels := map[string]string{
		"director.gamefleet.io/fleet": fleet.Name,
	}
	if err := r.List(ctx, &pods, client.InNamespace(fleet.Namespace), client.MatchingLabels(labels)); err != nil {
		return snapshot, FleetMetrics{}, err
	}

	var endpoints []string
	for _, pod := range pods.Items {
		if pod.Status.PodIP != "" && pod.Status.Phase == corev1.PodRunning {
			endpoints = append(endpoints, r.metricsEndpoint(pod.Status.PodIP, fleet))
		}
	}

	agg, err := r.Scraper.ScrapeFleet(ctx, endpoints)
	if err != nil {
		return snapshot, FleetMetrics{}, err
	}

	var warmReplicas int32
	for _, pod := range pods.Items {
		phase := pod.Labels["director.gamefleet.io/phase"]
		switch phase {
		case "allocated":
			snapshot.allocatedReplicas++
		case "draining":
			snapshot.drainingReplicas++
		default:
			if pod.Status.Phase == corev1.PodRunning {
				warmReplicas++
			}
		}
	}

	snapshot.replicas = int32(len(pods.Items))
	snapshot.readyReplicas = warmReplicas + snapshot.allocatedReplicas
	snapshot.bufferPool = warmReplicas
	snapshot.totalPlayers = agg.TotalPlayers

	return snapshot, FleetMetrics{
		TotalPlayers:    agg.TotalPlayers,
		AverageCPU:      agg.AvgCPU,
		AverageMemoryMB: agg.AvgMemoryMB,
	}, nil
}

// applyStatusSnapshot copies computed status values onto the fleet object.
func (r *FleetReconciler) applyStatusSnapshot(fleet *v1alpha1.GameServerFleet, s fleetStatusSnapshot) {
	fleet.Status.Replicas = s.replicas
	fleet.Status.AllocatedReplicas = s.allocatedReplicas
	fleet.Status.DrainingReplicas = s.drainingReplicas
	fleet.Status.ReadyReplicas = s.readyReplicas
	fleet.Status.BufferPool = s.bufferPool
	fleet.Status.TotalPlayers = s.totalPlayers
}

func (r *FleetReconciler) executeScaleUp(ctx context.Context, fleet *v1alpha1.GameServerFleet, result *ScaleResult, dryRun bool) error {
	if dryRun {
		log.Printf("[DRY-RUN] [fleet-controller] %s: would scale up by %d (from %d to %d)",
			fleet.Name, result.Delta, result.CurrentReplicas, result.DesiredReplicas)
		return nil
t	r.recordScaleDown(fleet.Name)
	}

	log.Printf("[OK] [fleet-controller] %s: SCALE_UP +%d (%d → %d) — %s",
		fleet.Name, result.Delta, result.CurrentReplicas, result.DesiredReplicas, result.Reason)

	for i := int32(0); i < result.Delta; i++ {
		pod := r.buildGameServerPod(fleet)
		if err := r.Create(ctx, pod); err != nil {
			return fmt.Errorf("create pod %d/%d: %w", i+1, result.Delta, err)
		}
		r.PoolMgr.Register(fleet.Name, pod.Name)
	}

	r.recordScaleUp(fleet.Name)
	return nil
}

func (r *FleetReconciler) executeScaleDown(ctx context.Context, fleet *v1alpha1.GameServerFleet, result *ScaleResult, policy *v1alpha1.AutoscalerPolicy, dryRun bool) error {
	if dryRun {
		log.Printf("[DRY-RUN] [fleet-controller] %s: would scale down by %d (from %d to %d)",
			fleet.Name, -result.Delta, result.CurrentReplicas, result.DesiredReplicas)
		return nil
	}
t	r.recordScaleDown(fleet.Name)

	idleServers := r.PoolMgr.IdleServers(fleet.Name)
	drainCount := -result.Delta
	if int32(len(idleServers)) < drainCount {
		drainCount = int32(len(idleServers))
	}

	if drainCount == 0 {
		log.Printf("[INFO] [fleet-controller] %s: no idle servers to drain", fleet.Name)
		r.recordScaleDown(fleet.Name)
		return nil
	}

	log.Printf("[OK] [fleet-controller] %s: SCALE_DOWN -%d (%d → %d) — draining %d idle servers",
		fleet.Name, drainCount, result.CurrentReplicas, result.DesiredReplicas, drainCount)

	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	if r.drainStates == nil {
		r.drainStates = make(map[string]*drainer.ServerState)
	}

	for i := int32(0); i < drainCount; i++ {
		serverName := idleServers[i]

		// Build the endpoint for session queries.
		var pod corev1.Pod
		key := client.ObjectKey{Name: serverName, Namespace: fleet.Namespace}
		endpoint := serverName + fmt.Sprintf(":%d", findMetricsPort(fleet.Spec.Template.Spec))
		if err := r.Get(ctx, key, &pod); err == nil && pod.Status.PodIP != "" {
			endpoint = pod.Status.PodIP + fmt.Sprintf(":%d", findMetricsPort(fleet.Spec.Template.Spec))
		}

		// Update Pod label to "draining" so state survives controller restart.
		if err := r.patchPodPhase(ctx, &pod, "draining"); err != nil {
			log.Printf("[WARN] [fleet-controller] %s: label update for %s failed: %v", fleet.Name, serverName, err)
		}

		// Mark server as unhealthy in pool so it won't be allocated again.
		r.PoolMgr.MarkUnhealthy(fleet.Name, serverName)

		// Start the drain and store the state with the real endpoint.
		ds := r.Drainer.StartDrain(serverName, endpoint)
		r.drainStates[serverName] = ds
		log.Printf("[OK] [fleet-controller] %s: drain initiated for %s (phase=%s, endpoint=%s)", fleet.Name, serverName, ds.Phase, endpoint)
	}

	r.recordScaleDown(fleet.Name)
	return nil
}

// advanceDraining iterates all in-progress drains, advances the state machine,
// and deletes Pods that have completed draining.
func (r *FleetReconciler) advanceDraining(ctx context.Context, fleet *v1alpha1.GameServerFleet) {
	r.drainMu.Lock()
	defer r.drainMu.Unlock()

	if r.drainStates == nil {
		return
	}

	for serverName, ds := range r.drainStates {
		// Only advance if it's time to check.
		if !r.Drainer.ShouldCheck(ds) && ds.Phase != drainer.PhaseCordon {
			continue
		}

		readyForDecom, err := r.Drainer.Advance(ctx, ds)
		if err != nil {
			log.Printf("[WARN] [fleet-controller] %s: drain advance for %s failed: %v", fleet.Name, serverName, err)
			continue
		}

		if !readyForDecom {
			continue
		}

		// Drain complete — delete the Pod.
		pod := &corev1.Pod{}
		pod.SetName(serverName)
		pod.SetNamespace(fleet.Namespace)

		if err := r.Delete(ctx, pod); err != nil {
			log.Printf("[ERROR] [fleet-controller] %s: delete pod %s: %v", fleet.Name, serverName, err)
			continue
		}

		log.Printf("[OK] [fleet-controller] %s: pod %s deleted (drain complete, phase=%s)", fleet.Name, serverName, ds.Phase)

		// Record drain duration.
		drainSecs := time.Since(ds.EnteredAt).Seconds()
		metrics.DrainDuration.WithLabelValues(fleet.Name).Observe(drainSecs)

		// Clean up.
		r.PoolMgr.Remove(fleet.Name, serverName)
		delete(r.drainStates, serverName)
	}
}

// patchPodPhase updates the director.gamefleet.io/phase label on a Pod.
func (r *FleetReconciler) patchPodPhase(ctx context.Context, pod *corev1.Pod, phase string) error {
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels["director.gamefleet.io/phase"] = phase
	return r.Patch(ctx, pod, patch)
}

func (r *FleetReconciler) buildGameServerPod(fleet *v1alpha1.GameServerFleet) *corev1.Pod {
	labels := map[string]string{
		"director.gamefleet.io/fleet": fleet.Name,
		"director.gamefleet.io/phase": "warm",
		"app":                         "game-server",
	}
	for k, v := range fleet.Spec.Template.Labels {
		labels[k] = v
	}

	controller := true
	blockOwnerDeletion := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fleet.Name + "-",
			Namespace:    fleet.Namespace,
			Labels:       labels,
			Annotations:  fleet.Spec.Template.Annotations,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         v1alpha1.SchemeGroupVersion.String(),
					Kind:               "GameServerFleet",
					Name:               fleet.Name,
					UID:                fleet.UID,
					Controller:         &controller,
					BlockOwnerDeletion: &blockOwnerDeletion,
				},
			},
		},
		Spec: fleet.Spec.Template.Spec,
	}

	if pod.Spec.NodeSelector == nil {
		pod.Spec.NodeSelector = fleet.Spec.NodeSelector
	}

	return pod
}

func (r *FleetReconciler) updateStatus(ctx context.Context, fleet *v1alpha1.GameServerFleet, result *ScaleResult) error {
	condition := v1alpha1.FleetCondition{
		Type:               v1alpha1.FleetAvailable,
		Status:             metav1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	}

	switch result.Decision {
	case ScaleFrozen:
		condition.Type = v1alpha1.FleetCircuitOpen
		condition.Status = metav1.ConditionTrue
		condition.Reason = "CircuitBreakerOpen"
		condition.Message = result.Reason
	case ScaleUp, ScaleDown:
		condition.Type = v1alpha1.FleetScaling
		condition.Status = metav1.ConditionTrue
		condition.Message = result.Reason
	}

	fleet.Status.Conditions = []v1alpha1.FleetCondition{condition}
	return r.Status().Update(ctx, fleet)
}

// findMetricsPort returns the port number for HTTP-based endpoints (metrics, health, sessions)
// on game server Pods. It prefers a port named "metrics" or "http", falls back to the first TCP
// port, and defaults to 8080.
func findMetricsPort(spec corev1.PodSpec) int32 {
	for _, c := range spec.Containers {
		for _, p := range c.Ports {
			if p.ContainerPort > 0 && (p.Name == "metrics" || p.Name == "http") {
				return p.ContainerPort
			}
		}
	}
	for _, c := range spec.Containers {
		for _, p := range c.Ports {
			if p.ContainerPort > 0 && p.Protocol == corev1.ProtocolTCP {
				return p.ContainerPort
			}
		}
	}
	if len(spec.Containers) > 0 && len(spec.Containers[0].Ports) > 0 {
		if p := spec.Containers[0].Ports[0].ContainerPort; p > 0 {
			return p
		}
	}
	return 8080
}

// metricsEndpoint builds the HTTP endpoint from a Pod IP and the fleet's pod template.
func (r *FleetReconciler) metricsEndpoint(ip string, fleet *v1alpha1.GameServerFleet) string {
	port := findMetricsPort(fleet.Spec.Template.Spec)
	return fmt.Sprintf("%s:%d", ip, port)
}

// --- Circuit Breaker ---

func (r *FleetReconciler) recordFailure(fleetName string) {
	r.cbMu.Lock()
	defer r.cbMu.Unlock()

	if r.consecutiveFailures == nil {
		r.consecutiveFailures = make(map[string]int)
	}
	r.consecutiveFailures[fleetName]++

	if r.consecutiveFailures[fleetName] >= 5 {
		if r.circuitOpenUntil == nil {
			r.circuitOpenUntil = make(map[string]time.Time)
		}
		r.circuitOpenUntil[fleetName] = time.Now().Add(5 * time.Minute)
		metrics.CircuitBreakerTriggered.WithLabelValues(fleetName, "default").Inc()
		log.Printf("[WARN] [fleet-controller] %s: circuit breaker OPEN (%d consecutive failures)", fleetName, r.consecutiveFailures[fleetName])
	}
}

func (r *FleetReconciler) resetCircuit(fleetName string) {
	r.cbMu.Lock()
	defer r.cbMu.Unlock()
	delete(r.consecutiveFailures, fleetName)
	delete(r.circuitOpenUntil, fleetName)
}

func (r *FleetReconciler) isCircuitOpen(fleetName string) bool {
	r.cbMu.RLock()
	defer r.cbMu.RUnlock()

	if r.circuitOpenUntil == nil {
		return false
	}
	until, ok := r.circuitOpenUntil[fleetName]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(r.circuitOpenUntil, fleetName)
		log.Printf("[INFO] [fleet-controller] %s: circuit breaker HALF-OPEN (probing)", fleetName)
		return false
	}
	return true
}

func (r *FleetReconciler) cooldownRemaining(fleetName string, cfg *v1alpha1.CooldownConfig, isScaleUp bool) time.Duration {
	r.cooldownMu.Lock()
	defer r.cooldownMu.Unlock()

	var last time.Time
	var ok bool
	var windowSecs int32
	if isScaleUp {
		if r.lastScaleUpAt == nil {
			return 0
		}
		last, ok = r.lastScaleUpAt[fleetName]
		windowSecs = cfg.ScaleUpSeconds
	} else {
		if r.lastScaleDownAt == nil {
			return 0
		}
		last, ok = r.lastScaleDownAt[fleetName]
		windowSecs = cfg.ScaleDownSeconds
	}
	if !ok || windowSecs <= 0 {
		return 0
	}
	elapsed := time.Since(last)
	window := time.Duration(windowSecs) * time.Second
	if elapsed < window {
		return (window - elapsed).Truncate(time.Second)
	}
	return 0
}

func (r *FleetReconciler) recordScaleUp(fleetName string) {
	r.cooldownMu.Lock()
	defer r.cooldownMu.Unlock()
	if r.lastScaleUpAt == nil {
		r.lastScaleUpAt = make(map[string]time.Time)
	}
	r.lastScaleUpAt[fleetName] = time.Now()
}

func (r *FleetReconciler) recordScaleDown(fleetName string) {
	r.cooldownMu.Lock()
	defer r.cooldownMu.Unlock()
	if r.lastScaleDownAt == nil {
		r.lastScaleDownAt = make(map[string]time.Time)
	}
	r.lastScaleDownAt[fleetName] = time.Now()
}
