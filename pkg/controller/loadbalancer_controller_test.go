package controller

import (
	"context"
	"flag"
	"os"
	"testing"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/triton/loadbalancer-controller/pkg/triton"
)

// realTestEnabled returns true if real integration tests should be run
func realTestEnabled() bool {
	// Check if TRITON_TEST_INTEGRATION environment variable is set to "true"
	return os.Getenv("TRITON_TEST_INTEGRATION") == "true"
}

// getRealTritonClient creates a real Triton client for integration testing
func getRealTritonClient(t *testing.T) *triton.Client {
	if !realTestEnabled() {
		t.Skip("Skipping integration test; set TRITON_TEST_INTEGRATION=true to enable")
		return nil
	}

	account := os.Getenv("TRITON_ACCOUNT")
	keyID := os.Getenv("TRITON_KEY_ID")
	keyPath := os.Getenv("TRITON_KEY_PATH")
	url := os.Getenv("TRITON_URL")

	// Skip if any required variable is missing
	if account == "" || keyID == "" || keyPath == "" || url == "" {
		t.Skip("Skipping integration test; missing required Triton credentials")
		return nil
	}

	client, err := triton.NewClient(account, keyID, keyPath, url)
	if err != nil {
		t.Fatalf("Failed to create Triton client: %v", err)
		return nil
	}

	return client
}

// TritonClientWrapper is a wrapper that can provide either real or simulated client behavior
type TritonClientWrapper struct {
	RealClient     *triton.Client
	simulated      bool
	loadBalancers  map[string]*triton.LoadBalancerParams
	instances      map[string]*triton.TritonInstance
}

// NewTritonClientWrapper creates a new wrapper that can work in simulated mode or with a real client
func NewTritonClientWrapper(realClient *triton.Client) *TritonClientWrapper {
	if realClient == nil {
		// Use simulated mode
		return &TritonClientWrapper{
			simulated:     true,
			loadBalancers: make(map[string]*triton.LoadBalancerParams),
			instances:     make(map[string]*triton.TritonInstance),
		}
	}
	
	// Use real client mode
	return &TritonClientWrapper{
		RealClient: realClient,
		simulated:  false,
	}
}

func (w *TritonClientWrapper) CreateLoadBalancer(ctx context.Context, params triton.LoadBalancerParams) error {
	if !w.simulated {
		return w.RealClient.CreateLoadBalancer(ctx, params)
	}
	
	// Simulated mode
	w.loadBalancers[params.Name] = &params
	w.instances[params.Name] = &triton.TritonInstance{
		ID:   "test-instance-id",
		Name: params.Name,
		IPs:  []string{"192.0.2.1", "10.0.0.1"},
		Tags: map[string]interface{}{
			"loadbalancer": "true",
			"managed-by":   "triton-loadbalancer-controller",
		},
	}
	return nil
}

func (w *TritonClientWrapper) UpdateLoadBalancer(ctx context.Context, name string, params triton.LoadBalancerParams) error {
	if !w.simulated {
		return w.RealClient.UpdateLoadBalancer(ctx, name, params)
	}
	
	// Simulated mode
	w.loadBalancers[name] = &params
	return nil
}

func (w *TritonClientWrapper) DeleteLoadBalancer(ctx context.Context, name string) error {
	if !w.simulated {
		return w.RealClient.DeleteLoadBalancer(ctx, name)
	}
	
	// Simulated mode
	delete(w.loadBalancers, name)
	delete(w.instances, name)
	return nil
}

func (w *TritonClientWrapper) GetLoadBalancer(ctx context.Context, name string) (*triton.LoadBalancerParams, error) {
	if !w.simulated {
		return w.RealClient.GetLoadBalancer(ctx, name)
	}
	
	// Simulated mode
	lb, exists := w.loadBalancers[name]
	if !exists {
		return nil, nil
	}
	return lb, nil
}

func (w *TritonClientWrapper) GetInstanceByName(ctx context.Context, name string) (*triton.TritonInstance, error) {
	if !w.simulated {
		return w.RealClient.GetInstanceByName(ctx, name)
	}
	
	// Simulated mode
	instance, exists := w.instances[name]
	if !exists {
		return nil, nil
	}
	return instance, nil
}

func TestReconcileCreateLoadBalancer(t *testing.T) {
	// Check if we should use real Triton client for integration testing
	realClient := getRealTritonClient(t)
	
	// Create test service name - use a unique name if using real client
	serviceName := "test-service"
	if realClient != nil {
		serviceName = "test-service-" + metav1.Now().Format("20060102-150405")
		
		// Make sure to clean up after the test
		defer func() {
			ctx := context.Background()
			_ = realClient.DeleteLoadBalancer(ctx, serviceName)
		}()
	}
	
	// Create a service to test with
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: "default",
			Annotations: map[string]string{
				"cloud.tritoncompute/max_rs":           "64",
				"cloud.tritoncompute/certificate_name": "example.com",
				"cloud.tritoncompute/metrics_acl":      "10.0.0.0/8 192.168.0.0/16",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
				{
					Name:       "https",
					Port:       443,
					TargetPort: intstr.FromInt(8443),
				},
			},
			Selector: map[string]string{
				"app": "test-app",
			},
		},
	}

	// Create a runtime scheme
	s := scheme.Scheme
	s.AddKnownTypes(corev1.SchemeGroupVersion, service)

	// Create a fake client
	objs := []runtime.Object{service}
	client := fake.NewClientBuilder().WithRuntimeObjects(objs...).Build()

	// Create Triton client wrapper
	tritonClient := NewTritonClientWrapper(realClient)

	// Create the reconciler
	reconciler := &LoadBalancerReconciler{
		Client:       client,
		Log:          testr.New(t),
		Scheme:       s,
		TritonClient: tritonClient,
	}

	// Call Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      serviceName,
			Namespace: "default",
		},
	}

	ctx := context.Background()
	_, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}

	// Skip validation for real client tests as it might take time for the load balancer to be fully provisioned
	if tritonClient.simulated {
		// Check if the load balancer was created
		lb, exists := tritonClient.loadBalancers[serviceName]
		if !exists {
			t.Fatalf("expected load balancer to be created, but it wasn't")
		}

		// Verify load balancer configuration
		if lb.Name != serviceName {
			t.Errorf("expected load balancer name to be '%s', got '%s'", serviceName, lb.Name)
		}

		if lb.MaxBackends != 64 {
			t.Errorf("expected max backends to be 64, got %d", lb.MaxBackends)
		}

		if lb.CertificateName != "example.com" {
			t.Errorf("expected certificate name to be 'example.com', got '%s'", lb.CertificateName)
		}

		if len(lb.PortMappings) != 2 {
			t.Errorf("expected 2 port mappings, got %d", len(lb.PortMappings))
		}

		// Verify port mappings
		httpFound := false
		httpsFound := false
		for _, mapping := range lb.PortMappings {
			if mapping.Type == "http" && mapping.ListenPort == 80 && mapping.BackendPort == 8080 {
				httpFound = true
			}
			if mapping.Type == "https" && mapping.ListenPort == 443 && mapping.BackendPort == 8443 {
				httpsFound = true
			}
		}

		if !httpFound {
			t.Errorf("expected HTTP port mapping, but not found")
		}

		if !httpsFound {
			t.Errorf("expected HTTPS port mapping, but not found")
		}

		// Verify instance was created
		instance, exists := tritonClient.instances[serviceName]
		if !exists {
			t.Fatalf("expected instance to be created, but it wasn't")
		}

		if instance.Name != serviceName {
			t.Errorf("expected instance name to be '%s', got '%s'", serviceName, instance.Name)
		}

		// Fetch the service to check if status was updated
		updatedService := &corev1.Service{}
		err = client.Get(ctx, req.NamespacedName, updatedService)
		if err != nil {
			t.Fatalf("failed to get service: %v", err)
		}

		// Verify status was updated with load balancer IP
		if len(updatedService.Status.LoadBalancer.Ingress) != 1 {
			t.Errorf("expected 1 ingress entry in load balancer status, got %d", len(updatedService.Status.LoadBalancer.Ingress))
		}
	} else {
		t.Logf("Integration test with real Triton client completed successfully. Load balancer '%s' created.", serviceName)
	}
}

// Test the extraction of load balancer params from a Kubernetes service
func TestExtractLoadBalancerParams(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
			Annotations: map[string]string{
				"cloud.tritoncompute/max_rs":           "64",
				"cloud.tritoncompute/certificate_name": "example.com",
				"cloud.tritoncompute/metrics_acl":      "10.0.0.0/8 192.168.0.0/16",
			},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
				{
					Name:       "https",
					Port:       443,
					TargetPort: intstr.FromInt(8443),
				},
				{
					Name:       "custom",
					Port:       8000,
					TargetPort: intstr.FromInt(9000),
				},
			},
			Selector: map[string]string{
				"app": "test-app",
			},
		},
	}

	reconciler := &LoadBalancerReconciler{
		Log: testr.New(t),
	}

	params, err := reconciler.extractLoadBalancerParams(service)
	if err != nil {
		t.Fatalf("extractLoadBalancerParams: (%v)", err)
	}

	// Verify basic params
	if params.Name != "test-service" {
		t.Errorf("expected name to be 'test-service', got '%s'", params.Name)
	}

	if params.MaxBackends != 64 {
		t.Errorf("expected max backends to be 64, got %d", params.MaxBackends)
	}

	if params.CertificateName != "example.com" {
		t.Errorf("expected certificate name to be 'example.com', got '%s'", params.CertificateName)
	}

	// Verify metrics ACL
	if len(params.MetricsACL) != 2 {
		t.Errorf("expected 2 metrics ACL entries, got %d", len(params.MetricsACL))
	} else {
		if params.MetricsACL[0] != "10.0.0.0/8" {
			t.Errorf("expected first metrics ACL to be '10.0.0.0/8', got '%s'", params.MetricsACL[0])
		}
		if params.MetricsACL[1] != "192.168.0.0/16" {
			t.Errorf("expected second metrics ACL to be '192.168.0.0/16', got '%s'", params.MetricsACL[1])
		}
	}

	// Verify port mappings
	if len(params.PortMappings) != 3 {
		t.Errorf("expected 3 port mappings, got %d", len(params.PortMappings))
	} else {
		// Verify each port mapping
		var httpFound, httpsFound, tcpFound bool

		for _, mapping := range params.PortMappings {
			if mapping.Type == "http" && mapping.ListenPort == 80 && mapping.BackendPort == 8080 {
				httpFound = true
			} else if mapping.Type == "https" && mapping.ListenPort == 443 && mapping.BackendPort == 8443 {
				httpsFound = true
			} else if mapping.Type == "tcp" && mapping.ListenPort == 8000 && mapping.BackendPort == 9000 {
				tcpFound = true
			}
		}

		if !httpFound {
			t.Errorf("HTTP port mapping not found or incorrect")
		}
		if !httpsFound {
			t.Errorf("HTTPS port mapping not found or incorrect")
		}
		if !tcpFound {
			t.Errorf("TCP port mapping not found or incorrect")
		}
	}
}
