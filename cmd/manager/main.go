package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
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

	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager.")
	flag.StringVar(&tritonKeyPath, "triton-key-path", "", "Path to the Triton private key.")
	flag.StringVar(&tritonKeyId, "triton-key-id", "", "Triton key ID for API authentication.")
	flag.StringVar(&tritonAccount, "triton-account", "", "Triton account name.")
	flag.StringVar(&tritonUrl, "triton-url", "", "Triton CloudAPI URL.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	// Create manager options compatible with older controller-runtime versions
	opts := ctrl.Options{
		Scheme:           scheme,
		LeaderElection:   enableLeaderElection,
		LeaderElectionID: "triton-loadbalancer-controller",
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), opts)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Initialize Triton client
	tritonClient, err := triton.NewClient(tritonAccount, tritonKeyId, tritonKeyPath, tritonUrl)
	if err != nil {
		setupLog.Error(err, "unable to create Triton client")
		os.Exit(1)
	}

	if err = controller.NewLoadBalancerReconciler(
		mgr.GetClient(),
		ctrl.Log.WithName("controllers").WithName("LoadBalancer"),
		mgr.GetScheme(),
		tritonClient,
	).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "LoadBalancer")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
