package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/api"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/controller"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/pool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	scheme = runtime.NewScheme()
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		apiAddr     string
		metricsAddr string
		rateLimit   int
		tlsCertFile string
		tlsKeyFile  string
		logLevel    string
	)

	flag.StringVar(&apiAddr, "api-addr", ":8443", "Allocation API listen address")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "Metrics / healthz listener address")
	flag.IntVar(&rateLimit, "rate-limit", 100, "Max allocation requests per second")
	flag.StringVar(&tlsCertFile, "tls-cert-file", "", "Path to TLS certificate file (enables HTTPS)")
	flag.StringVar(&tlsKeyFile, "tls-key-file", "", "Path to TLS private key file (enables HTTPS)")
	flag.Parse()

	// Apply environment variable overrides.
	apiAddr = envOrStrApiserver("GFD_API_ADDR", apiAddr)
	metricsAddr = envOrStrApiserver("GFD_METRICS_ADDR", metricsAddr)
	if v := os.Getenv("GFD_RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rateLimit = n
		}
	}
	if v := os.Getenv("GFD_LOG_LEVEL"); v != "" {
		logLevel = v
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{
		Development: logLevel == "debug",
	})))

	// Create a lightweight manager for K8s client access only.
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: metricsAddr,
	})
	if err != nil {
		log.Fatalf("[FATAL] unable to create manager: %v", err)
	}

	poolMgr := pool.NewPoolManager(nil)

	// The AllocationReconciler also implements the Allocator interface.
	allocReconciler := &controller.AllocationReconciler{
		Client:  mgr.GetClient(),
		Scheme:  scheme,
		PoolMgr: poolMgr,
	}

	// Build HTTP handler.
	mw := api.NewMiddleware(rateLimit)
	handler := api.NewHandler(allocReconciler, poolMgr, mw)

	// Start API server.
	apiServer := api.NewServer(apiAddr, handler, tlsCertFile, tlsKeyFile)

	// Start the manager in background (for client access).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := mgr.Start(ctx); err != nil {
			log.Fatalf("[FATAL] manager stopped: %v", err)
		}
	}()

	// Wait for cache to sync before populating the pool.
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		log.Fatalf("[FATAL] cache sync timed out")
	}

	// Rebuild pool state from existing Pods so the REST API can serve allocations.
	if err := syncPoolsFromLabels(ctx, mgr.GetClient(), poolMgr); err != nil {
		log.Printf("[WARN] [apiserver] initial pool sync: %v", err)
	}

	// Watch for GameServerAllocation CRDs.
	if err := allocReconciler.SetupWithManager(mgr); err != nil {
		log.Fatalf("[FATAL] setup alloc reconciler: %v", err)
	}

	// Periodic pool sync to keep REST API pool in sync with reality.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := syncPoolsFromLabels(ctx, mgr.GetClient(), poolMgr); err != nil {
					log.Printf("[WARN] [apiserver] periodic pool sync: %v", err)
				}
			}
		}
	}()

	// Start API server.
	go func() {
		if err := apiServer.Start(); err != nil {
			log.Fatalf("[FATAL] api server stopped: %v", err)
		}
	}()
	log.Printf("[INFO] allocation API server started on %s (rate limit: %d/s)", apiAddr, rateLimit)

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Printf("[INFO] shutting down gracefully...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := apiServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("[ERROR] api server shutdown: %v", err)
	}

	cancel()
	log.Printf("[INFO] shutdown complete")
}

func envOrStrApiserver(envVar, fallback string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return fallback
}

// syncPoolsFromLabels rebuilds the in-memory pool state by listing all Pods
// across all namespaces that belong to a GameServerFleet.
func syncPoolsFromLabels(ctx context.Context, cli client.Client, pm *pool.PoolManager) error {
	var pods corev1.PodList
	if err := cli.List(ctx, &pods, client.HasLabels{"director.gamefleet.io/fleet"}); err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		fleetName := pod.Labels["director.gamefleet.io/fleet"]
		if fleetName == "" {
			continue
		}
		pm.Register(fleetName, pod.Name)
		switch pod.Labels["director.gamefleet.io/phase"] {
		case "allocated":
			pm.MarkAllocated(fleetName, pod.Name)
		case "draining":
			pm.MarkUnhealthy(fleetName, pod.Name)
		default:
			pm.MarkAvailable(fleetName, pod.Name)
		}
	}
	return nil
}
