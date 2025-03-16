package controller

import (
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

// mockTritonClient is a mock implementation of the Triton client for testing
type mockTritonClient struct {
	loadBalancers map[string]*triton.LoadBalancerParams
	instances     map[string]*triton.TritonInstance
}

func newMockTritonClient() *mockTritonClient {
	return &mockTritonClient{
		loadBalancers: make(map[string]*triton.LoadBalancerParams),
		instances:     make(map[string]*triton.TritonInstance),
	}
}

func (m *mockTritonClient) CreateLoadBalancer(ctx interface{}, params triton.LoadBalancerParams) error {
	m.loadBalancers[params.Name] = &params
	m.instances[params.Name] = &triton.TritonInstance{
		ID:   "test-instance-id",
		Name: params.Name,
		IPs:  []string{"192.0.2.1", "10.0.0.1"},
		Tags: map[string]string{
			"loadbalancer": "true",
			"managed-by":   "triton-loadbalancer-controller",
		},
	}
	return nil
}

func (m *mockTritonClient) UpdateLoadBalancer(ctx interface{}, name string, params triton.LoadBalancerParams) error {
	m.loadBalancers[name] = &params
	return nil
}

func (m *mockTritonClient) DeleteLoadBalancer(ctx interface{}, name string) error {
	delete(m.loadBalancers, name)
	delete(m.instances, name)
	return nil
}

func (m *mockTritonClient) GetLoadBalancer(ctx interface{}, name string) (*triton.LoadBalancerParams, error) {
	lb, exists := m.loadBalancers[name]
	if !exists {
		return nil, nil
	}
	return lb, nil
}

func (m *mockTritonClient) GetInstanceByName(ctx interface{}, name string) (*triton.TritonInstance, error) {
	instance, exists := m.instances[name]
	if !exists {
		return nil, nil
	}
	return instance, nil
}

func TestReconcileCreateLoadBalancer(t *testing.T) {
	// Create a service to test with
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
			Annotations: map[string]string{
				"cloud.tritoncompute/max_rs":          "64",
				"cloud.tritoncompute/certificate_name": "example.com",
				"cloud.tritoncompute/metrics_acl":     "10.0.0.0/8 192.168.0.0/16",
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

	// Create a mock Triton client
	mockClient := newMockTritonClient()

	// Create the reconciler
	reconciler := &LoadBalancerReconciler{
		Client:       client,
		Log:          testr.New(t),
		Scheme:       s,
		TritonClient: mockClient,
	}

	// Call Reconcile
	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "test-service",
			Namespace: "default",
		},
	}

	_, err := reconciler.Reconcile(req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}

	// Check if the load balancer was created
	lb, exists := mockClient.loadBalancers["test-service"]
	if !exists {
		t.Fatalf("expected load balancer to be created, but it wasn't")
	}

	// Verify load balancer configuration
	if lb.Name != "test-service" {
		t.Errorf("expected load balancer name to be 'test-service', got '%s'", lb.Name)
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
	instance, exists := mockClient.instances["test-service"]
	if !exists {
		t.Fatalf("expected instance to be created, but it wasn't")
	}

	if instance.Name != "test-service" {
		t.Errorf("expected instance name to be 'test-service', got '%s'", instance.Name)
	}

	// Fetch the service to check if status was updated
	updatedService := &corev1.Service{}
	err = client.Get(req.NamespacedName, updatedService)
	if err != nil {
		t.Fatalf("failed to get service: %v", err)
	}

	// Verify status was updated with load balancer IP
	if len(updatedService.Status.LoadBalancer.Ingress) != 1 {
		t.Errorf("expected 1 ingress entry in load balancer status, got %d", len(updatedService.Status.LoadBalancer.Ingress))
	}
}