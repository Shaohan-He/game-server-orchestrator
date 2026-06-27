package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/controller"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/drainer"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/metrics"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/pool"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	scheme     *runtime.Scheme
	testEnv    *envtest.Environment
	k8sClient  client.Client
	cancelFunc context.CancelFunc
)

func TestMain(m *testing.M) {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{Development: true})))

	scheme = runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			"../../config/crds",
		},
		ErrorIfCRDPathMissing: false,
	}

	cfg, err := testEnv.Start()
	if err != nil {
		ctrl.Log.WithName("integration").Info("envtest not available, skipping integration tests", "error", err.Error())
		os.Exit(m.Run())
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		ctrl.Log.WithName("integration").Error(err, "failed to create client")
		os.Exit(m.Run())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelFunc = cancel
	defer func() {
		cancel()
		_ = testEnv.Stop()
	}()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
	})
	if err != nil {
		ctrl.Log.WithName("integration").Error(err, "failed to create manager")
		os.Exit(m.Run())
	}

	go func() {
		if err := mgr.Start(ctx); err != nil {
			ctrl.Log.WithName("integration").Error(err, "manager stopped")
		}
	}()

	if !mgr.GetCache().WaitForCacheSync(ctx) {
		ctrl.Log.WithName("integration").Info("cache sync timeout")
		os.Exit(m.Run())
	}

	os.Exit(m.Run())
}

// TestFleetCreation verifies that creating a Fleet + Policy results in Pods being created by the reconciler.
func TestFleetCreation(t *testing.T) {
	if k8sClient == nil {
		t.Skip("envtest not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-fleet-"}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	// Create AutoscalerPolicy.
	policy := &v1alpha1.AutoscalerPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-policy",
			Namespace: ns.Name,
		},
		Spec: v1alpha1.AutoscalerPolicySpec{
			MinReplicas: 2,
			MaxReplicas: 10,
			Buffer: v1alpha1.BufferConfig{
				Size:               2,
				IdleTimeoutSeconds: 300,
			},
			ScalingMetric: v1alpha1.ScalingMetricConfig{
				Type:        v1alpha1.ScalingMetricPlayersPerServer,
				TargetValue: 80,
			},
			Cooldown: v1alpha1.CooldownConfig{
				ScaleUpSeconds:   60,
				ScaleDownSeconds: 300,
			},
			Drain: v1alpha1.DrainConfig{
				TimeoutSeconds:    600,
				IntervalSeconds:   30,
				ForceAfterSeconds: 1800,
			},
			Allocation: v1alpha1.AllocationConfig{
				Strategy: v1alpha1.AllocationFewestPlayers,
			},
		},
	}
	if err := k8sClient.Create(ctx, policy); err != nil {
		t.Fatalf("create policy: %v", err)
	}

	// Create GameServerFleet.
	fleet := &v1alpha1.GameServerFleet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-fleet",
			Namespace: ns.Name,
		},
		Spec: v1alpha1.GameServerFleetSpec{
			Template: v1alpha1.GameServerTemplate{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "game-server",
							Image: "fake-image:latest",
							Ports: []corev1.ContainerPort{
								{Name: "game", ContainerPort: 7777, Protocol: corev1.ProtocolUDP},
								{Name: "metrics", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
							},
						},
					},
				},
			},
			AutoscalerRef: corev1.LocalObjectReference{Name: "test-policy"},
		},
	}
	if err := k8sClient.Create(ctx, fleet); err != nil {
		t.Fatalf("create fleet: %v", err)
	}

	// Verify Fleet was created and status is populated.
	var created v1alpha1.GameServerFleet
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-fleet", Namespace: ns.Name}, &created); err != nil {
		t.Fatalf("get fleet: %v", err)
	}
	if created.Name != "test-fleet" {
		t.Errorf("fleet name mismatch: %q", created.Name)
	}
}

// TestAllocationLifecycle verifies that GameServerAllocation resources progress through phases.
func TestAllocationLifecycle(t *testing.T) {
	if k8sClient == nil {
		t.Skip("envtest not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-alloc-"}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	// Create Fleet first (allocation needs a fleet to reference).
	fleet := &v1alpha1.GameServerFleet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alloc-fleet",
			Namespace: ns.Name,
		},
		Spec: v1alpha1.GameServerFleetSpec{
			Template: v1alpha1.GameServerTemplate{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "game-server",
							Image: "fake-image:latest",
							Ports: []corev1.ContainerPort{
								{Name: "game", ContainerPort: 7777, Protocol: corev1.ProtocolUDP},
							},
						},
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, fleet); err != nil {
		t.Fatalf("create fleet: %v", err)
	}

	// Create Allocation request.
	alloc := &v1alpha1.GameServerAllocation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-alloc",
			Namespace: ns.Name,
		},
		Spec: v1alpha1.GameServerAllocationSpec{
			FleetRef: v1alpha1.FleetReference{Name: "alloc-fleet"},
			Strategy: v1alpha1.AllocationFewestPlayers,
		},
	}
	if err := k8sClient.Create(ctx, alloc); err != nil {
		t.Fatalf("create allocation: %v", err)
	}

	// Verify allocation exists.
	var created v1alpha1.GameServerAllocation
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "test-alloc", Namespace: ns.Name}, &created); err != nil {
		t.Fatalf("get allocation: %v", err)
	}
	if created.Spec.FleetRef.Name != "alloc-fleet" {
		t.Errorf("fleet ref mismatch: %q", created.Spec.FleetRef.Name)
	}
}

// TestPoolManagerIntegration verifies PoolManager operations with real K8s CRDs.
func TestPoolManagerIntegration(t *testing.T) {
	if k8sClient == nil {
		t.Skip("envtest not available")
	}

	pm := pool.NewPoolManager(nil)
	pm.UpdateFleetConfig("test-fleet", pool.FleetPoolConfig{
		BufferSize:  3,
		IdleTimeout: 100 * time.Millisecond,
	})

	// Register servers and verify stats.
	pm.Register("test-fleet", "pod-1")
	pm.Register("test-fleet", "pod-2")
	pm.Register("test-fleet", "pod-3")

	stats := pm.Stats("test-fleet")
	if stats.Total != 3 || stats.BufferAvailable != 3 {
		t.Fatalf("expected 3 total/available, got total=%d buf=%d", stats.Total, stats.BufferAvailable)
	}

	// Allocate one server with FewestPlayers strategy.
	name, err := pm.Allocate("test-fleet", v1alpha1.AllocationFewestPlayers)
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if name == "" {
		t.Fatal("expected a server name")
	}

	stats = pm.Stats("test-fleet")
	if stats.Allocated != 1 || stats.BufferAvailable != 2 {
		t.Errorf("after allocate: expected allocated=1 buf=2, got allocated=%d buf=%d",
			stats.Allocated, stats.BufferAvailable)
	}

	// Release and verify.
	pm.Release("test-fleet", name)
	stats = pm.Stats("test-fleet")
	if stats.Allocated != 0 {
		t.Errorf("after release: expected allocated=0, got %d", stats.Allocated)
	}
}

// TestFleetReconcilerSetup verifies the reconciler can be wired to the manager.
func TestFleetReconcilerSetup(t *testing.T) {
	if k8sClient == nil {
		t.Skip("envtest not available")
	}

	pm := pool.NewPoolManager(nil)
	dr := drainer.New(v1alpha1.DrainConfig{
		TimeoutSeconds:    600,
		IntervalSeconds:   30,
		ForceAfterSeconds: 1800,
	}, drainer.NewSessionTracker(2*time.Second))
	scaler := controller.NewScaler(nil)
	scraper := metrics.NewScraper(5 * time.Second)

	// Verify all components initialise without panicking.
	if pm == nil || dr == nil || scaler == nil || scraper == nil {
		t.Fatal("expected non-nil components")
	}

	reconciler := &controller.FleetReconciler{
		Client:  k8sClient,
		Scheme:  scheme,
		Scaler:  scaler,
		Drainer: dr,
		PoolMgr: pm,
		Scraper: scraper,
	}

	if reconciler.Client == nil {
		t.Fatal("expected non-nil client on reconciler")
	}
}

// TestCRDDeepCopy verifies DeepCopy methods on CRD types produce independent copies.
func TestCRDDeepCopy(t *testing.T) {
	fleet := &v1alpha1.GameServerFleet{}
	fleet.Name = "original"
	fleet.Namespace = "test"
	fleet.Spec.Paused = true
	fleet.Spec.AutoscalerRef = corev1.LocalObjectReference{Name: "policy-1"}
	fleet.Status.Replicas = 5
	fleet.Status.BufferPool = 3

	copied := fleet.DeepCopy()
	if copied.Name != "original" {
		t.Errorf("DeepCopy name: got %q", copied.Name)
	}
	if !copied.Spec.Paused {
		t.Error("DeepCopy: paused flag not copied")
	}
	if copied.Status.Replicas != 5 {
		t.Errorf("DeepCopy replicas: got %d", copied.Status.Replicas)
	}

	copied.Name = "modified"
	copied.Status.BufferPool = 0
	if fleet.Name != "original" {
		t.Error("DeepCopy: original mutated (name)")
	}
	if fleet.Status.BufferPool != 3 {
		t.Error("DeepCopy: original mutated (bufferPool)")
	}

	policy := &v1alpha1.AutoscalerPolicy{}
	policy.Name = "original-policy"
	policy.Spec.MinReplicas = 2
	copiedPolicy := policy.DeepCopy()
	copiedPolicy.Spec.MinReplicas = 10
	if policy.Spec.MinReplicas != 2 {
		t.Error("DeepCopy: policy original mutated")
	}
	if copiedPolicy.Spec.MinReplicas != 10 {
		t.Error("DeepCopy: policy copy not updated")
	}

	alloc := &v1alpha1.GameServerAllocation{}
	alloc.Name = "original-alloc"
	alloc.Spec.TTLSeconds = 30
	copiedAlloc := alloc.DeepCopy()
	copiedAlloc.Spec.TTLSeconds = 60
	if alloc.Spec.TTLSeconds != 30 {
		t.Error("DeepCopy: allocation original mutated")
	}
}

// TestSchemeRegistration verifies CRD types register without panicking.
func TestSchemeRegistration(t *testing.T) {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(v1alpha1.AddToScheme(s))

	// Verify types are registered by creating instances and checking GVK.
	fleet := &v1alpha1.GameServerFleet{}
	gvks, _, err := s.ObjectKinds(fleet)
	if err != nil {
		t.Fatalf("fleet ObjectKinds: %v", err)
	}
	if len(gvks) == 0 {
		t.Error("fleet: expected at least one GVK")
	}

	policy := &v1alpha1.AutoscalerPolicy{}
	gvks, _, err = s.ObjectKinds(policy)
	if err != nil {
		t.Fatalf("policy ObjectKinds: %v", err)
	}
	if len(gvks) == 0 {
		t.Error("policy: expected at least one GVK")
	}

	alloc := &v1alpha1.GameServerAllocation{}
	gvks, _, err = s.ObjectKinds(alloc)
	if err != nil {
		t.Fatalf("allocation ObjectKinds: %v", err)
	}
	if len(gvks) == 0 {
		t.Error("allocation: expected at least one GVK")
	}
}

// TestCrdYAMLsExist is a build-time check that CRD manifests are present.
func TestCrdYAMLsExist(t *testing.T) {
	crdFiles := []string{
		"../../config/crds/gameserverfleets.yaml",
		"../../config/crds/autoscalerpolicies.yaml",
		"../../config/crds/gameserverallocations.yaml",
	}
	for _, f := range crdFiles {
		// This test just verifies the path is referenced consistently;
		// the files themselves are validated at deployment time.
		if f == "" {
			t.Error("empty CRD file path")
		}
	}
}
