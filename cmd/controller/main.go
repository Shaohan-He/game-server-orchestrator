package main

import (
	"flag"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/Shaohan-He/game-server-orchestrator/api/v1alpha1"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/controller"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/drainer"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/health"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/metrics"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/notifier"
	"github.com/Shaohan-He/game-server-orchestrator/pkg/pool"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
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

// envOrStr returns the value of envVar if set, otherwise returns fallback.
func envOrStr(envVar, fallback string) string {
	if v := os.Getenv(envVar); v != "" {
		return v
	}
	return fallback
}

// envOrDur returns the parsed duration from envVar if set, otherwise returns fallback.
func envOrDur(envVar string, fallback time.Duration) time.Duration {
	if v := os.Getenv(envVar); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("[WARN] invalid duration %q for %s, using default %s", v, envVar, fallback)
	}
	return fallback
}

// envOrBool returns the parsed bool from envVar if set, otherwise returns fallback.
func envOrBool(envVar string, fallback bool) bool {
	if v := os.Getenv(envVar); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		log.Printf("[WARN] invalid bool %q for %s, using default %v", v, envVar, fallback)
	}
	return fallback
}

func main() {
	var (
		metricsAddr       string
		namespace         string
		resyncPeriod      time.Duration
		leaderElect       bool
		nhwEndpoint       string
		nhwTimeout        time.Duration
		notifyFeishuURL   string
		notifyDingtalkURL string
		logLevel          string
	)

	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "Metrics / healthz listener address")
	flag.StringVar(&namespace, "namespace", "game-fleet-system", "Controller deployment namespace")
	flag.DurationVar(&resyncPeriod, "resync-period", 15*time.Second, "Fleet reconcile interval")
	flag.BoolVar(&leaderElect, "leader-elect", true, "Enable leader election for HA")
	flag.StringVar(&nhwEndpoint, "nhw-endpoint", "", "Node Health Watcher API endpoint (empty disables)")
	flag.DurationVar(&nhwTimeout, "nhw-timeout", 10*time.Second, "NHW query timeout")
	flag.StringVar(&notifyFeishuURL, "notify-feishu-url", "", "Feishu webhook URL")
	flag.StringVar(&notifyDingtalkURL, "notify-dingtalk-url", "", "DingTalk webhook URL")
	flag.StringVar(&logLevel, "log-level", "info", "Log level (debug/info/warn/error)")
	flag.Parse()

	// Apply environment variable overrides (GFD_* vars take precedence over CLI defaults).
	metricsAddr = envOrStr("GFD_METRICS_ADDR", metricsAddr)
	namespace = envOrStr("GFD_NAMESPACE", namespace)
	resyncPeriod = envOrDur("GFD_RESYNC_PERIOD", resyncPeriod)
	leaderElect = envOrBool("GFD_LEADER_ELECT", leaderElect)
	nhwEndpoint = envOrStr("GFD_NHW_ENDPOINT", nhwEndpoint)
	nhwTimeout = envOrDur("GFD_NHW_TIMEOUT", nhwTimeout)
	notifyFeishuURL = envOrStr("GFD_NOTIFY_FEISHU_URL", notifyFeishuURL)
	notifyDingtalkURL = envOrStr("GFD_NOTIFY_DINGTALK_URL", notifyDingtalkURL)
	logLevel = envOrStr("GFD_LOG_LEVEL", logLevel)

	// Set up structured logging.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	if logLevel == "debug" {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{
		Development: logLevel == "debug",
	})))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:  metricsAddr,
		LeaderElection:          leaderElect,
		LeaderElectionID:        "game-fleet-director-controller",
		LeaderElectionNamespace: namespace,
	})
	if err != nil {
		log.Fatalf("[FATAL] unable to create manager: %v", err)
	}

	// Build health providers.
	var hp health.HealthProvider
	if nhwEndpoint != "" {
		hp = health.NewNHWClient(nhwEndpoint, nhwTimeout)
		log.Printf("[INFO] node health watcher integration enabled: %s", nhwEndpoint)
	}

	nodeFilter := health.NewNodeFilter(hp)

	poolMgr := pool.NewPoolManager(nodeFilter)
	drainTracker := drainer.NewSessionTracker(5 * time.Second)

	// Build a default drain config; real config is loaded per-fleet from policy.
	defaultDrain := v1alpha1.DrainConfig{
		TimeoutSeconds:    600,
		IntervalSeconds:   30,
		ForceAfterSeconds: 1800,
	}
	dr := drainer.New(defaultDrain, drainTracker)

	// Build scaler.
	scaler := controller.NewScaler(nodeFilter)

	// Build notifier.
	ntf := notifier.NewNotifier(notifyFeishuURL, notifyDingtalkURL)

	// Build metrics scraper.
	scraper := metrics.NewScraper(5 * time.Second)

	// Register Fleet Reconciler.
	fleetReconciler := &controller.FleetReconciler{
		Client:   mgr.GetClient(),
		Scheme:   scheme,
		Scaler:   scaler,
		Drainer:  dr,
		PoolMgr:  poolMgr,
		Notifier: ntf,
		Scraper:  scraper,
	}
	if err := fleetReconciler.SetupWithManager(mgr); err != nil {
		log.Fatalf("[FATAL] unable to create fleet controller: %v", err)
	}

	// Register Allocation Reconciler.
	allocReconciler := &controller.AllocationReconciler{
		Client:  mgr.GetClient(),
		Scheme:  scheme,
		PoolMgr: poolMgr,
	}
	if err := allocReconciler.SetupWithManager(mgr); err != nil {
		log.Fatalf("[FATAL] unable to create allocation controller: %v", err)
	}

	log.Printf("[INFO] starting game fleet director controller (resync=%s)", resyncPeriod)
	log.Printf("[INFO] metrics → %s", metricsAddr)

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Fatalf("[FATAL] controller stopped: %v", err)
	}
}
