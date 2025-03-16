package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/triton/loadbalancer-controller/pkg/triton"
)

// LoadBalancerReconciler reconciles a Service object with type LoadBalancer
type LoadBalancerReconciler struct {
	client.Client
	Log         logr.Logger
	Scheme      *runtime.Scheme
	TritonClient *triton.Client
}

// NewLoadBalancerReconciler creates a new LoadBalancerReconciler
func NewLoadBalancerReconciler(client client.Client, log logr.Logger, scheme *runtime.Scheme, tritonClient *triton.Client) *LoadBalancerReconciler {
	return &LoadBalancerReconciler{
		Client:       client,
		Log:          log,
		Scheme:       scheme,
		TritonClient: tritonClient,
	}
}

// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core,resources=services/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile handles Service updates and creates/updates/deletes Triton load balancers as needed
func (r *LoadBalancerReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("service", req.NamespacedName)

	// Fetch the Service instance
	var service corev1.Service
	if err := r.Get(ctx, req.NamespacedName, &service); err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			log.Info("Service resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	// Only process LoadBalancer type services
	if service.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return ctrl.Result{}, nil
	}

	// Handle deletion
	if !service.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &service)
	}

	// Handle creation/update
	return r.reconcileNormal(ctx, &service)
}

// reconcileNormal handles the creation and update of load balancers
func (r *LoadBalancerReconciler) reconcileNormal(ctx context.Context, service *corev1.Service) (ctrl.Result, error) {
	log := r.Log.WithValues("service", fmt.Sprintf("%s/%s", service.Namespace, service.Name))
	log.Info("Reconciling LoadBalancer service")

	// Extract load balancer configuration from service
	lbParams, err := r.extractLoadBalancerParams(service)
	if err != nil {
		log.Error(err, "Failed to extract load balancer parameters")
		return ctrl.Result{}, err
	}

	// Check if the load balancer already exists
	existingLB, err := r.TritonClient.GetLoadBalancer(ctx, service.Name)
	if err != nil {
		log.Error(err, "Failed to check if load balancer exists")
		return ctrl.Result{}, err
	}

	if existingLB == nil {
		// Create new load balancer
		log.Info("Creating new load balancer", "name", service.Name)
		if err := r.TritonClient.CreateLoadBalancer(ctx, lbParams); err != nil {
			log.Error(err, "Failed to create load balancer")
			return ctrl.Result{}, err
		}
		log.Info("Successfully created load balancer", "name", service.Name)
	} else {
		// Update existing load balancer
		log.Info("Updating existing load balancer", "name", service.Name)
		if err := r.TritonClient.UpdateLoadBalancer(ctx, service.Name, lbParams); err != nil {
			log.Error(err, "Failed to update load balancer")
			return ctrl.Result{}, err
		}
		log.Info("Successfully updated load balancer", "name", service.Name)
	}

	// Get load balancer instance to extract IP information
	loadBalancer, err := r.TritonClient.GetLoadBalancer(ctx, service.Name)
	if err != nil {
		log.Error(err, "Failed to get load balancer info for status update")
		return ctrl.Result{}, err
	}

	// Get the load balancer IP address
	lbInstance, err := r.TritonClient.GetInstanceByName(ctx, service.Name)
	if err != nil {
		log.Error(err, "Failed to get load balancer instance for IP")
		return ctrl.Result{}, err
	}

	// Update service status with load balancer information
	if lbInstance != nil && len(lbInstance.IPs) > 0 {
		// Copy current status
		updatedService := service.DeepCopy()
		
		// Find a public IP address in the list
		var lbIP string
		for _, ip := range lbInstance.IPs {
			// Prefer non-private IP address
			if !strings.HasPrefix(ip, "10.") && !strings.HasPrefix(ip, "192.168.") && !strings.HasPrefix(ip, "172.") {
				lbIP = ip
				break
			}
		}
		
		// Use private IP if no public one is found
		if lbIP == "" && len(lbInstance.IPs) > 0 {
			lbIP = lbInstance.IPs[0]
		}
		
		// Update the load balancer status
		if lbIP != "" {
			updatedService.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
				{
					IP: lbIP,
				},
			}
			
			// Update status subresource
			if err := r.Status().Update(ctx, updatedService); err != nil {
				log.Error(err, "Failed to update Service status with load balancer IP")
				return ctrl.Result{}, err
			}
			
			log.Info("Updated service status with load balancer IP", "ip", lbIP)
		}
	}

	return ctrl.Result{}, nil
}

// reconcileDelete handles the deletion of load balancers
func (r *LoadBalancerReconciler) reconcileDelete(ctx context.Context, service *corev1.Service) (ctrl.Result, error) {
	log := r.Log.WithValues("service", fmt.Sprintf("%s/%s", service.Namespace, service.Name))
	log.Info("Reconciling LoadBalancer service deletion")

	// Delete load balancer
	if err := r.TritonClient.DeleteLoadBalancer(ctx, service.Name); err != nil {
		log.Error(err, "Failed to delete load balancer")
		return ctrl.Result{}, err
	}

	log.Info("Successfully deleted load balancer", "name", service.Name)
	return ctrl.Result{}, nil
}

// extractLoadBalancerParams extracts load balancer configuration from a Service
func (r *LoadBalancerReconciler) extractLoadBalancerParams(service *corev1.Service) (triton.LoadBalancerParams, error) {
	params := triton.LoadBalancerParams{
		Name: service.Name,
	}

	// Extract port mappings from service ports
	for _, port := range service.Spec.Ports {
		// Determine protocol type (http, https, tcp)
		portType := "tcp"
		if port.Name == "http" || port.Port == 80 {
			portType = "http"
		} else if port.Name == "https" || port.Port == 443 {
			portType = "https"
		}

		mapping := triton.PortMapping{
			Type:        portType,
			ListenPort:  int(port.Port),
			BackendName: service.Name,
			BackendPort: int(port.TargetPort.IntVal),
		}
		params.PortMappings = append(params.PortMappings, mapping)
	}

	// Extract additional configuration from annotations
	annotations := service.Annotations
	
	// Check for max_rs
	if maxRS, ok := annotations["cloud.tritoncompute/max_rs"]; ok {
		if maxRSInt, err := strconv.Atoi(maxRS); err == nil {
			params.MaxBackends = maxRSInt
		}
	}

	// Check for certificate_name
	if certName, ok := annotations["cloud.tritoncompute/certificate_name"]; ok {
		params.CertificateName = certName
	}

	// Check for metrics_acl
	if metricsACL, ok := annotations["cloud.tritoncompute/metrics_acl"]; ok {
		// Split by commas or spaces
		var aclList []string
		for _, acl := range strings.FieldsFunc(metricsACL, func(r rune) bool {
			return r == ',' || r == ' '
		}) {
			if acl != "" {
				aclList = append(aclList, acl)
			}
		}
		params.MetricsACL = aclList
	}

	return params, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *LoadBalancerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		Complete(r)
}