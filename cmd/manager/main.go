package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/triton/loadbalancer-controller/pkg/controller"
	"github.com/triton/loadbalancer-controller/pkg/triton"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var tritonKeyPath string
	var tritonKeyId string
	var tritonAccount string
	var tritonUrl string
	var probeAddr string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager.")
	flag.StringVar(&tritonKeyPath, "triton-key-path", "", "Path to the Triton private key.")
	flag.StringVar(&tritonKeyId, "triton-key-id", "", "Triton key ID for API authentication.")
	flag.StringVar(&tritonAccount, "triton-account", "", "Triton account name.")
	flag.StringVar(&tritonUrl, "triton-url", "", "Triton CloudAPI URL.")
	flag.Parse()

	// Validate required flags
	if tritonKeyPath == "" || tritonKeyId == "" || tritonAccount == "" || tritonUrl == "" {
		setupLog.Error(nil, "Missing required Triton credentials",
			"keyPath", tritonKeyPath != "",
			"keyId", tritonKeyId != "",
			"account", tritonAccount != "",
			"url", tritonUrl != "")
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))

	// Create manager - use simple version for now
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "triton-loadbalancer-controller",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Initialize Triton client
	setupLog.Info("Initializing Triton client",
		"account", tritonAccount,
		"keyId", tritonKeyId,
		"keyPath", tritonKeyPath,
		"url", tritonUrl)

	// Check for optional environment variables
	if pkg := os.Getenv("TRITON_LB_PACKAGE"); pkg != "" {
		setupLog.Info("Using custom load balancer package", "package", pkg)
	}

	if img := os.Getenv("TRITON_LB_IMAGE"); img != "" {
		setupLog.Info("Using custom load balancer image", "image", img)
	}

	// Initialize client
	tritonClient, err := triton.NewClient(tritonAccount, tritonKeyId, tritonKeyPath, tritonUrl)
	if err != nil {
		setupLog.Error(err, "unable to create Triton client")
		os.Exit(1)
	}

	setupLog.Info("Triton client initialized successfully")

	if err = controller.NewLoadBalancerReconciler(
		mgr.GetClient(),
		ctrl.Log.WithName("controllers").WithName("LoadBalancer"),
		mgr.GetScheme(),
		tritonClient,
	).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "LoadBalancer")
		os.Exit(1)
	}

	// Add health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
