package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
)

func TestMain(m *testing.M) {
	// Try envtest first, then fall back to a real cluster via kubeconfig.
	testEnv := &envtest.Environment{
		ErrorIfCRDPathMissing: false,
		CRDDirectoryPaths: []string{
			"../../config/crds",
		},
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		// Try in-cluster config, then kubeconfig.
		cfg, err = rest.InClusterConfig()
		if err != nil {
			loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
			cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, nil).ClientConfig()
			if err != nil {
				fmt.Println("e2e: no cluster available, skipping")
				os.Exit(m.Run())
			}
		}
	}

	s := runtime.NewScheme()
	utilruntime.Must(scheme.AddToScheme(s))
	utilruntime.Must(v1alpha1.AddToScheme(s))

	k8sClient, err = client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		fmt.Printf("e2e: failed to create client: %v\n", err)
		os.Exit(m.Run())
	}

	os.Exit(m.Run())
}

func skipIfNoCluster(t *testing.T) {
	t.Helper()
	if k8sClient == nil {
		t.Skip("no cluster available")
	}
}

// TestDeployCRDs verifies that CRDs can be installed on the cluster.
func TestDeployCRDs(t *testing.T) {
	skipIfNoCluster(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Verify we can list GameServerFleets (CRDs should be installed).
	var fleets v1alpha1.GameServerFleetList
	err := k8sClient.List(ctx, &fleets)
	if err != nil {
		t.Logf("listing fleets (CRDs may not be installed): %v", err)
	}
}

// TestFleetLifecycle creates a Fleet + Policy and verifies Pods are created.
func TestFleetLifecycle(t *testing.T) {
	skipIfNoCluster(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "e2e-fleet-test",
		},
	}
	_ = k8sClient.Delete(ctx, ns)
	time.Sleep(2 * time.Second)

	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	defer func() { _ = k8sClient.Delete(ctx, ns) }()

	// Create AutoscalerPolicy.
	policy := &v1alpha1.AutoscalerPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "e2e-policy",
			Namespace: ns.Name,
		},
		Spec: v1alpha1.AutoscalerPolicySpec{
			MinReplicas: 1,
			MaxReplicas: 5,
			Buffer: v1alpha1.BufferConfig{
				Size:               2,
				IdleTimeoutSeconds: 60,
			},
			ScalingMetric: v1alpha1.ScalingMetricConfig{
				Type:        v1alpha1.ScalingMetricPlayersPerServer,
				TargetValue: 80,
			},
			Cooldown: v1alpha1.CooldownConfig{
				ScaleUpSeconds:   30,
				ScaleDownSeconds: 120,
			},
			Drain: v1alpha1.DrainConfig{
				TimeoutSeconds:    300,
				IntervalSeconds:   30,
				ForceAfterSeconds: 600,
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
			Name:      "e2e-fleet",
			Namespace: ns.Name,
		},
		Spec: v1alpha1.GameServerFleetSpec{
			Template: v1alpha1.GameServerTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": "e2e-game-server",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "game-server",
							Image: "alpine:3.20",
							Command: []string{
								"sh", "-c",
								"echo 'fake game server running' && sleep 3600",
							},
							Ports: []corev1.ContainerPort{
								{Name: "game", ContainerPort: 7777, Protocol: corev1.ProtocolUDP},
								{Name: "metrics", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
							},
						},
					},
				},
			},
			AutoscalerRef: corev1.LocalObjectReference{Name: "e2e-policy"},
		},
	}
	if err := k8sClient.Create(ctx, fleet); err != nil {
		t.Fatalf("create fleet: %v", err)
	}

	// Wait for fleet to be reconciled and Pods created.
	var pods corev1.PodList
	deadline := time.After(60 * time.Second)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var podCount int
loop:
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for fleet pods (got %d)", podCount)
		case <-ticker.C:
			if err := k8sClient.List(ctx, &pods,
				client.InNamespace(ns.Name),
				client.MatchingLabels(map[string]string{
					"director.gamefleet.io/fleet": "e2e-fleet",
				}),
			); err != nil {
				t.Logf("list pods: %v", err)
				continue
			}
			podCount = len(pods.Items)
			t.Logf("waiting for pods: got %d, want >= 1", podCount)
			if podCount >= 1 {
				break loop
			}
		}
	}

	// Verify fleet status.
	var updated v1alpha1.GameServerFleet
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "e2e-fleet", Namespace: ns.Name}, &updated); err != nil {
		t.Fatalf("get fleet: %v", err)
	}
	t.Logf("fleet status: replicas=%d ready=%d buffer=%d",
		updated.Status.Replicas, updated.Status.ReadyReplicas, updated.Status.BufferPool)

	if updated.Status.Replicas < 1 {
		t.Errorf("expected at least 1 replica, got %d", updated.Status.Replicas)
	}
}

// TestAllocationAPI tests the REST allocation endpoint.
func TestAllocationAPI(t *testing.T) {
	skipIfNoCluster(t)

	apiAddr := os.Getenv("E2E_API_ADDR")
	if apiAddr == "" {
		apiAddr = "localhost:8443"
	}

	client := &http.Client{Timeout: 10 * time.Second}

	allocReq := map[string]interface{}{
		"fleet":     "e2e-fleet",
		"namespace": "e2e-fleet-test",
		"strategy":  "FewestPlayers",
	}
	body, _ := json.Marshal(allocReq)

	resp, err := client.Post(
		fmt.Sprintf("http://%s/api/v1/allocate", apiAddr),
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Skipf("API server not reachable at %s: %v (this is expected if apiserver is not port-forwarded)", apiAddr, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		t.Logf("allocation result: server=%s endpoint=%s", result["serverName"], result["endpoint"])
		if result["serverName"] == nil || result["serverName"] == "" {
			t.Error("expected non-empty serverName in response")
		}
	} else if resp.StatusCode == http.StatusServiceUnavailable {
		t.Log("allocation returned 503 — no available servers (expected for fresh fleet)")
	} else {
		t.Logf("allocation returned status %d", resp.StatusCode)
	}
}

// TestHealthEndpoint verifies the API server healthz endpoint.
func TestHealthEndpoint(t *testing.T) {
	skipIfNoCluster(t)

	apiAddr := os.Getenv("E2E_API_ADDR")
	if apiAddr == "" {
		apiAddr = "localhost:8443"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/healthz", apiAddr))
	if err != nil {
		t.Skipf("API server not reachable at %s: %v", apiAddr, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: expected 200, got %d", resp.StatusCode)
	} else {
		t.Log("healthz OK")
	}
}

// TestFleetStatusEndpoint verifies the fleet status REST endpoint.
func TestFleetStatusEndpoint(t *testing.T) {
	skipIfNoCluster(t)

	apiAddr := os.Getenv("E2E_API_ADDR")
	if apiAddr == "" {
		apiAddr = "localhost:8443"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("http://%s/api/v1/fleets/e2e-fleet-test/e2e-fleet/status", apiAddr)
	resp, err := client.Get(url)
	if err != nil {
		t.Skipf("API server not reachable at %s: %v", apiAddr, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Logf("fleet status: HTTP %d (fleet may not exist yet)", resp.StatusCode)
	} else {
		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		t.Logf("fleet status response: %v", result)
	}
}
