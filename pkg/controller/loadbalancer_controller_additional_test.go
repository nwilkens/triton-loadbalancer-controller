package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/triton/loadbalancer-controller/pkg/triton"
)

// MockTritonClient implements TritonClientInterface for testing
type MockTritonClient struct {
	createErr     error
	updateErr     error
	deleteErr     error
	getErr        error
	loadBalancers map[string]*triton.LoadBalancerParams
	instances     map[string]*triton.TritonInstance
	createCalled  int
	updateCalled  int
	deleteCalled  int
	getCalled     int
}

func NewMockTritonClient() *MockTritonClient {
	return &MockTritonClient{
		loadBalancers: make(map[string]*triton.LoadBalancerParams),
		instances:     make(map[string]*triton.TritonInstance),
	}
}

func (m *MockTritonClient) CreateLoadBalancer(ctx context.Context, params triton.LoadBalancerParams) error {
	m.createCalled++
	if m.createErr != nil {
		return m.createErr
	}
	m.loadBalancers[params.Name] = &params
	m.instances[params.Name] = &triton.TritonInstance{
		ID:   "test-id",
		Name: params.Name,
		IPs:  []string{"203.0.113.1", "10.0.0.1"},
	}
	return nil
}

func (m *MockTritonClient) UpdateLoadBalancer(ctx context.Context, name string, params triton.LoadBalancerParams) error {
	m.updateCalled++
	if m.updateErr != nil {
		return m.updateErr
	}
	m.loadBalancers[name] = &params
	return nil
}

func (m *MockTritonClient) DeleteLoadBalancer(ctx context.Context, name string) error {
	m.deleteCalled++
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.loadBalancers, name)
	delete(m.instances, name)
	return nil
}

func (m *MockTritonClient) GetLoadBalancer(ctx context.Context, name string) (*triton.LoadBalancerParams, error) {
	m.getCalled++
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.loadBalancers[name], nil
}

func (m *MockTritonClient) GetInstanceByName(ctx context.Context, name string) (*triton.TritonInstance, error) {
	return m.instances[name], nil
}

// TestReconcileDeleteLoadBalancer tests deletion of load balancers
func TestReconcileDeleteLoadBalancer(t *testing.T) {
	// Create a service with deletion timestamp
	deletionTime := metav1.NewTime(time.Now())
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-service",
			Namespace:         "default",
			DeletionTimestamp: &deletionTime,
			Finalizers:        []string{"loadbalancer.triton.io/finalizer"},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
			},
		},
	}

	// Create runtime scheme and client
	s := scheme.Scheme
	s.AddKnownTypes(corev1.SchemeGroupVersion, service)
	client := fake.NewClientBuilder().WithRuntimeObjects(service).Build()

	// Create mock Triton client
	mockClient := NewMockTritonClient()
	mockClient.loadBalancers["test-service"] = &triton.LoadBalancerParams{Name: "test-service"}

	// Create reconciler
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

	ctx := context.Background()
	_, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}

	// Verify delete was called
	if mockClient.deleteCalled != 1 {
		t.Errorf("expected delete to be called once, got %d", mockClient.deleteCalled)
	}

	// Verify load balancer was deleted
	if _, exists := mockClient.loadBalancers["test-service"]; exists {
		t.Error("expected load balancer to be deleted")
	}

	// The finalizer removal happens during reconcile, so we don't need to verify it here
	// The service might have been garbage collected after finalizer removal
}

// TestReconcileUpdateLoadBalancer tests updating existing load balancers
func TestReconcileUpdateLoadBalancer(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
			Annotations: map[string]string{
				"cloud.tritoncompute/max_rs": "128",
			},
			Finalizers: []string{"loadbalancer.triton.io/finalizer"},
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
			},
		},
	}

	// Create runtime scheme and client
	s := scheme.Scheme
	s.AddKnownTypes(corev1.SchemeGroupVersion, service)
	client := fake.NewClientBuilder().WithRuntimeObjects(service).Build()

	// Create mock Triton client with existing load balancer
	mockClient := NewMockTritonClient()
	mockClient.loadBalancers["test-service"] = &triton.LoadBalancerParams{
		Name:        "test-service",
		MaxBackends: 64,
	}
	mockClient.instances["test-service"] = &triton.TritonInstance{
		ID:   "existing-id",
		Name: "test-service",
		IPs:  []string{"203.0.113.1"},
	}

	// Create reconciler
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

	ctx := context.Background()
	_, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}

	// Verify update was called
	if mockClient.updateCalled != 1 {
		t.Errorf("expected update to be called once, got %d", mockClient.updateCalled)
	}

	// Verify create was not called
	if mockClient.createCalled != 0 {
		t.Errorf("expected create not to be called, got %d", mockClient.createCalled)
	}

	// Verify load balancer was updated
	lb := mockClient.loadBalancers["test-service"]
	if lb.MaxBackends != 128 {
		t.Errorf("expected max backends to be updated to 128, got %d", lb.MaxBackends)
	}
}

// TestReconcileNonLoadBalancerService tests that non-LoadBalancer services are ignored
func TestReconcileNonLoadBalancerService(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{
				{
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
			},
		},
	}

	// Create runtime scheme and client
	s := scheme.Scheme
	s.AddKnownTypes(corev1.SchemeGroupVersion, service)
	client := fake.NewClientBuilder().WithRuntimeObjects(service).Build()

	// Create mock Triton client
	mockClient := NewMockTritonClient()

	// Create reconciler
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

	ctx := context.Background()
	_, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("reconcile: (%v)", err)
	}

	// Verify no Triton operations were performed
	if mockClient.createCalled != 0 || mockClient.updateCalled != 0 || mockClient.deleteCalled != 0 {
		t.Error("expected no Triton operations for non-LoadBalancer service")
	}
}

// TestReconcileTransientError tests retry behavior on transient errors
func TestReconcileTransientError(t *testing.T) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				{
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
			},
		},
	}

	// Create runtime scheme and client
	s := scheme.Scheme
	s.AddKnownTypes(corev1.SchemeGroupVersion, service)
	client := fake.NewClientBuilder().WithRuntimeObjects(service).Build()

	// Create mock Triton client that returns timeout error
	mockClient := NewMockTritonClient()
	mockClient.createErr = errors.New("connection timeout")

	// Create reconciler
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

	ctx := context.Background()
	result, err := reconciler.Reconcile(ctx, req)

	// Should not return error for transient failures
	if err != nil {
		t.Fatalf("expected no error for transient failure, got: %v", err)
	}

	// Should request requeue after delay
	if result.RequeueAfter != 30*time.Second {
		t.Errorf("expected requeue after 30s, got %v", result.RequeueAfter)
	}
}

// TestIsTransientError tests the transient error detection
func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "timeout error",
			err:      errors.New("connection timeout"),
			expected: true,
		},
		{
			name:     "connection refused",
			err:      errors.New("connection refused"),
			expected: true,
		},
		{
			name:     "rate limit error",
			err:      errors.New("rate limit exceeded"),
			expected: true,
		},
		{
			name:     "permanent error",
			err:      errors.New("invalid credentials"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTransientError(tt.err)
			if result != tt.expected {
				t.Errorf("isTransientError(%v) = %v, expected %v", tt.err, result, tt.expected)
			}
		})
	}
}

// TestExtractLoadBalancerParamsEdgeCases tests edge cases in parameter extraction
func TestExtractLoadBalancerParamsEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		ports       []corev1.ServicePort
		validate    func(t *testing.T, params triton.LoadBalancerParams)
	}{
		{
			name:        "no annotations",
			annotations: nil,
			ports: []corev1.ServicePort{
				{Port: 80, TargetPort: intstr.FromInt(8080)},
			},
			validate: func(t *testing.T, params triton.LoadBalancerParams) {
				if params.MaxBackends != 0 {
					t.Errorf("expected default max backends, got %d", params.MaxBackends)
				}
				if params.CertificateName != "" {
					t.Errorf("expected empty certificate name, got %s", params.CertificateName)
				}
			},
		},
		{
			name: "invalid max_rs",
			annotations: map[string]string{
				"cloud.tritoncompute/max_rs": "invalid",
			},
			ports: []corev1.ServicePort{
				{Port: 80, TargetPort: intstr.FromInt(8080)},
			},
			validate: func(t *testing.T, params triton.LoadBalancerParams) {
				if params.MaxBackends != 0 {
					t.Errorf("expected default max backends for invalid value, got %d", params.MaxBackends)
				}
			},
		},
		{
			name: "empty metrics ACL",
			annotations: map[string]string{
				"cloud.tritoncompute/metrics_acl": "",
			},
			ports: []corev1.ServicePort{
				{Port: 80, TargetPort: intstr.FromInt(8080)},
			},
			validate: func(t *testing.T, params triton.LoadBalancerParams) {
				if len(params.MetricsACL) != 0 {
					t.Errorf("expected empty metrics ACL, got %v", params.MetricsACL)
				}
			},
		},
		{
			name:        "TCP port detection",
			annotations: nil,
			ports: []corev1.ServicePort{
				{Name: "tcp", Port: 3306, TargetPort: intstr.FromInt(3306)},
				{Port: 5432, TargetPort: intstr.FromInt(5432)},
			},
			validate: func(t *testing.T, params triton.LoadBalancerParams) {
				for _, pm := range params.PortMappings {
					if pm.Type != "tcp" {
						t.Errorf("expected TCP type for port %d, got %s", pm.ListenPort, pm.Type)
					}
				}
			},
		},
	}

	reconciler := &LoadBalancerReconciler{
		Log: testr.New(t),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-service",
					Annotations: tt.annotations,
				},
				Spec: corev1.ServiceSpec{
					Ports: tt.ports,
				},
			}

			params, err := reconciler.extractLoadBalancerParams(service)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			tt.validate(t, params)
		})
	}
}
