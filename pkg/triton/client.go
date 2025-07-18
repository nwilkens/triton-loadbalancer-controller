package triton

import (
	"context"
	"encoding/pem"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	triton "github.com/joyent/triton-go/v2"
	"github.com/joyent/triton-go/v2/authentication"
	"github.com/joyent/triton-go/v2/compute"
	"github.com/joyent/triton-go/v2/network"
)

// Client wraps the Triton API clients and provides methods for interacting with load balancers
type Client struct {
	compute *compute.ComputeClient
	network *network.NetworkClient
}

// NewClient creates a new Triton client with the provided credentials
func NewClient(account, keyID, keyPath, url string) (*Client, error) {
	if account == "" {
		return nil, fmt.Errorf("Triton account name is required")
	}
	if keyID == "" {
		return nil, fmt.Errorf("Triton key ID is required")
	}
	if keyPath == "" {
		return nil, fmt.Errorf("Triton key path is required")
	}
	if url == "" {
		return nil, fmt.Errorf("Triton API URL is required")
	}

	// Read the SSH private key file
	privateKeyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key from %s: %v", keyPath, err)
	}

	// Parse the private key
	block, _ := pem.Decode(privateKeyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block containing private key, check if file is in valid PEM format")
	}

	// Check if it's an encrypted key
	if block.Headers["Proc-Type"] == "4,ENCRYPTED" {
		return nil, fmt.Errorf("encrypted private keys are not supported, please decrypt the key first")
	}

	// Create signer input
	input := authentication.PrivateKeySignerInput{
		KeyID:              keyID,
		PrivateKeyMaterial: privateKeyData,
		AccountName:        account,
	}

	signer, err := authentication.NewPrivateKeySigner(input)
	if err != nil {
		return nil, fmt.Errorf("failed to create private key signer: %v", err)
	}

	config := &triton.ClientConfig{
		TritonURL:   url,
		AccountName: account,
		Signers:     []authentication.Signer{signer},
	}

	computeClient, err := compute.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create compute client: %v", err)
	}

	networkClient, err := network.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create network client: %v", err)
	}

	// Verify connection with a simple API call
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = computeClient.Instances().List(ctx, &compute.ListInstancesInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Triton API at %s: %v", url, err)
	}

	return &Client{
		compute: computeClient,
		network: networkClient,
	}, nil
}

// LoadBalancerParams defines the parameters for creating a load balancer
type LoadBalancerParams struct {
	Name            string
	PortMappings    []PortMapping
	MaxBackends     int
	CertificateName string
	MetricsACL      []string
}

// PortMapping represents a port mapping configuration for the load balancer
type PortMapping struct {
	Type        string // http, https, or tcp
	ListenPort  int
	BackendName string
	BackendPort int
}

// CreateLoadBalancer creates a new load balancer in Triton
func (c *Client) CreateLoadBalancer(ctx context.Context, params LoadBalancerParams) error {
	// Implementation for creating a load balancer via Triton CloudAPI
	// This will include translating the LoadBalancerParams to the appropriate
	// Triton API calls for creating a machine with the correct metadata

	// Metadata we'll set for the load balancer
	metadata := map[string]interface{}{
		"cloud.tritoncompute:loadbalancer": "true",
	}

	// Build the portmap string from the port mappings
	// Format: "<type>://<listen port>:<backend name>[:<backend port>]"
	var portmap string
	for i, mapping := range params.PortMappings {
		if i > 0 {
			portmap += ","
		}

		// Convert integers to strings properly
		listenPortStr := strconv.Itoa(mapping.ListenPort)

		if mapping.BackendPort > 0 {
			backendPortStr := strconv.Itoa(mapping.BackendPort)
			portmap += mapping.Type + "://" + listenPortStr + ":" + mapping.BackendName + ":" + backendPortStr
		} else {
			portmap += mapping.Type + "://" + listenPortStr + ":" + mapping.BackendName
		}
	}
	metadata["cloud.tritoncompute:portmap"] = portmap

	if params.MaxBackends > 0 {
		metadata["cloud.tritoncompute:max_rs"] = strconv.Itoa(params.MaxBackends)
	}

	if params.CertificateName != "" {
		metadata["cloud.tritoncompute:certificate_name"] = params.CertificateName
	}

	if len(params.MetricsACL) > 0 {
		// Join the ACL entries with commas
		var aclString string
		for i, acl := range params.MetricsACL {
			if i > 0 {
				aclString += ","
			}
			aclString += acl
		}
		metadata["cloud.tritoncompute:metrics_acl"] = aclString
	}

	// Default values
	packageName := os.Getenv("TRITON_LB_PACKAGE")
	if packageName == "" {
		packageName = "g4-highcpu-1G"
	}

	imageId := os.Getenv("TRITON_LB_IMAGE")
	if imageId == "" {
		imageId = "70e3ae72-96b6-11ea-9274-2f3c66e8b2c4" // Default HAProxy image
	}

	// Use Triton API to create the load balancer as a machine
	createInput := &compute.CreateInstanceInput{
		Name:     params.Name,
		Package:  packageName,
		Image:    imageId,
		Metadata: metadata,
		Tags: map[string]interface{}{
			"k8s-service":  params.Name,
			"managed-by":   "triton-loadbalancer-controller",
			"loadbalancer": "true",
		},
	}

	instance, err := c.compute.Instances().Create(ctx, createInput)
	if err != nil {
		return err
	}

	// Get timeout settings from environment or use defaults
	timeoutSeconds := 300 // Default: 5 minutes
	if timeoutEnv := os.Getenv("TRITON_PROVISION_TIMEOUT"); timeoutEnv != "" {
		if parsedTimeout, err := strconv.Atoi(timeoutEnv); err == nil && parsedTimeout > 0 {
			timeoutSeconds = parsedTimeout
		}
	}

	// Calculate how many iterations needed with 10 second intervals
	maxIterations := timeoutSeconds / 10
	if maxIterations < 1 {
		maxIterations = 1
	}

	// Wait for the instance to be provisioned
	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for load balancer to provision")
		default:
			getInput := &compute.GetInstanceInput{
				ID: instance.ID,
			}

			currentInstance, err := c.compute.Instances().Get(ctx, getInput)
			if err != nil {
				return fmt.Errorf("error checking instance status: %v", err)
			}

			if currentInstance.State == "running" {
				return nil // Successfully provisioned
			}

			// Log progress
			if i%6 == 0 { // Every minute
				fmt.Printf("Load balancer %s still provisioning (state: %s), waiting...\n",
					params.Name, currentInstance.State)
			}

			time.Sleep(10 * time.Second)
		}
	}

	return fmt.Errorf("timed out waiting for load balancer to provision after %d seconds", timeoutSeconds)
}

// DeleteLoadBalancer deletes a load balancer in Triton
func (c *Client) DeleteLoadBalancer(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("load balancer name cannot be empty")
	}

	// Find instance by name
	listInput := &compute.ListInstancesInput{
		Name: name,
		Tags: map[string]interface{}{
			"loadbalancer": "true",
			"managed-by":   "triton-loadbalancer-controller",
		},
	}

	instances, err := c.compute.Instances().List(ctx, listInput)
	if err != nil {
		return fmt.Errorf("failed to list instances: %v", err)
	}

	if len(instances) == 0 {
		// Instance not found, nothing to delete
		return nil
	}

	// Delete the instance
	deleteInput := &compute.DeleteInstanceInput{
		ID: instances[0].ID,
	}

	err = c.compute.Instances().Delete(ctx, deleteInput)
	if err != nil {
		return fmt.Errorf("failed to delete instance %s: %v", instances[0].ID, err)
	}

	// Get timeout settings from environment or use defaults
	timeoutSeconds := 300 // Default: 5 minutes
	if timeoutEnv := os.Getenv("TRITON_DELETE_TIMEOUT"); timeoutEnv != "" {
		if parsedTimeout, err := strconv.Atoi(timeoutEnv); err == nil && parsedTimeout > 0 {
			timeoutSeconds = parsedTimeout
		}
	}

	// Calculate how many iterations needed with 10 second intervals
	maxIterations := timeoutSeconds / 10
	if maxIterations < 1 {
		maxIterations = 1
	}

	// Wait for the instance to be deleted (no longer appears in list)
	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for load balancer to be deleted")
		default:
			instances, err := c.compute.Instances().List(ctx, listInput)
			if err != nil {
				return fmt.Errorf("failed to check if instance was deleted: %v", err)
			}

			if len(instances) == 0 {
				// Instance successfully deleted
				return nil
			}

			// Log progress periodically
			if i%6 == 0 { // Every minute
				fmt.Printf("Waiting for load balancer %s to be deleted...\n", name)
			}

			// Sleep for 10 seconds before retrying
			time.Sleep(10 * time.Second)
		}
	}

	return fmt.Errorf("timed out waiting for load balancer %s to be deleted after %d seconds", name, timeoutSeconds)
}

// UpdateLoadBalancer updates an existing load balancer in Triton
func (c *Client) UpdateLoadBalancer(ctx context.Context, name string, params LoadBalancerParams) error {
	// Find instance by name
	listInput := &compute.ListInstancesInput{
		Name: name,
		Tags: map[string]interface{}{
			"loadbalancer": "true",
			"managed-by":   "triton-loadbalancer-controller",
		},
	}

	instances, err := c.compute.Instances().List(ctx, listInput)
	if err != nil {
		return err
	}

	if len(instances) == 0 {
		return fmt.Errorf("load balancer %s not found", name)
	}

	// Prepare metadata for update
	metadata := map[string]interface{}{
		"cloud.tritoncompute:loadbalancer": "true",
	}

	// Build the portmap string from the port mappings
	var portmap string
	for i, mapping := range params.PortMappings {
		if i > 0 {
			portmap += ","
		}

		listenPortStr := strconv.Itoa(mapping.ListenPort)

		if mapping.BackendPort > 0 {
			backendPortStr := strconv.Itoa(mapping.BackendPort)
			portmap += mapping.Type + "://" + listenPortStr + ":" + mapping.BackendName + ":" + backendPortStr
		} else {
			portmap += mapping.Type + "://" + listenPortStr + ":" + mapping.BackendName
		}
	}
	metadata["cloud.tritoncompute:portmap"] = portmap

	if params.MaxBackends > 0 {
		metadata["cloud.tritoncompute:max_rs"] = strconv.Itoa(params.MaxBackends)
	}

	if params.CertificateName != "" {
		metadata["cloud.tritoncompute:certificate_name"] = params.CertificateName
	}

	if len(params.MetricsACL) > 0 {
		var aclString string
		for i, acl := range params.MetricsACL {
			if i > 0 {
				aclString += ","
			}
			aclString += acl
		}
		metadata["cloud.tritoncompute:metrics_acl"] = aclString
	}

	// Update the instance metadata
	updateInput := &compute.UpdateMetadataInput{
		ID:       instances[0].ID,
		Metadata: metadata,
	}

	_, err = c.compute.Instances().UpdateMetadata(ctx, updateInput)
	if err != nil {
		return err
	}

	return nil
}

// GetLoadBalancer retrieves information about a load balancer
func (c *Client) GetLoadBalancer(ctx context.Context, name string) (*LoadBalancerParams, error) {
	// Find instance by name
	listInput := &compute.ListInstancesInput{
		Name: name,
		Tags: map[string]interface{}{
			"loadbalancer": "true",
			"managed-by":   "triton-loadbalancer-controller",
		},
	}

	instances, err := c.compute.Instances().List(ctx, listInput)
	if err != nil {
		return nil, err
	}

	if len(instances) == 0 {
		// No load balancer found with this name
		return nil, nil
	}

	// Get instance metadata to extract load balancer configuration
	getInput := &compute.GetInstanceInput{
		ID: instances[0].ID,
	}

	instance, err := c.compute.Instances().Get(ctx, getInput)
	if err != nil {
		return nil, err
	}

	params := &LoadBalancerParams{
		Name: name,
	}

	// Extract configuration from metadata
	if portmapVal, ok := instance.Metadata["cloud.tritoncompute:portmap"]; ok {
		// Parse portmap string
		if portmapStr, ok := portmapVal.(string); ok {
			portMappings := parsePortMap(portmapStr)
			params.PortMappings = portMappings
		}
	}

	if maxRSVal, ok := instance.Metadata["cloud.tritoncompute:max_rs"]; ok {
		if maxRSStr, ok := maxRSVal.(string); ok {
			if maxRS, err := strconv.Atoi(maxRSStr); err == nil {
				params.MaxBackends = maxRS
			}
		}
	}

	if certNameVal, ok := instance.Metadata["cloud.tritoncompute:certificate_name"]; ok {
		if certName, ok := certNameVal.(string); ok {
			params.CertificateName = certName
		}
	}

	if metricsACLVal, ok := instance.Metadata["cloud.tritoncompute:metrics_acl"]; ok {
		if metricsACL, ok := metricsACLVal.(string); ok {
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
	}

	return params, nil
}

// parsePortMap parses a port map string into PortMapping structs
func parsePortMap(portmapStr string) []PortMapping {
	var mappings []PortMapping

	// No special handling for invalid formats - they'll naturally result in an empty slice

	// Split by commas
	portmapEntries := strings.Split(portmapStr, ",")

	for _, entry := range portmapEntries {
		// Parse entry format: <type>://<listen port>:<backend name>[:<backend port>]
		parts := strings.SplitN(entry, "://", 2)
		if len(parts) != 2 {
			continue
		}

		portType := parts[0]

		portParts := strings.Split(parts[1], ":")
		if len(portParts) < 2 {
			continue
		}

		listenPort, err := strconv.Atoi(portParts[0])
		if err != nil {
			continue
		}

		backendName := portParts[1]

		var backendPort int
		if len(portParts) > 2 {
			backendPort, _ = strconv.Atoi(portParts[2])
		}

		mapping := PortMapping{
			Type:        portType,
			ListenPort:  listenPort,
			BackendName: backendName,
			BackendPort: backendPort,
		}

		mappings = append(mappings, mapping)
	}

	return mappings
}

// TritonInstance represents a Triton compute instance with necessary information
type TritonInstance struct {
	ID   string
	Name string
	IPs  []string
	Tags map[string]interface{}
}

// GetInstanceByName retrieves a Triton instance by name
func (c *Client) GetInstanceByName(ctx context.Context, name string) (*TritonInstance, error) {
	// Find instance by name and tags
	listInput := &compute.ListInstancesInput{
		Name: name,
		Tags: map[string]interface{}{
			"loadbalancer": "true",
			"managed-by":   "triton-loadbalancer-controller",
		},
	}

	instances, err := c.compute.Instances().List(ctx, listInput)
	if err != nil {
		return nil, err
	}

	if len(instances) == 0 {
		// No instance found with this name
		return nil, nil
	}

	// Get the instance details
	getInput := &compute.GetInstanceInput{
		ID: instances[0].ID,
	}

	instance, err := c.compute.Instances().Get(ctx, getInput)
	if err != nil {
		return nil, err
	}

	// Extract IP addresses from networks
	var ips []string
	for _, ip := range instance.IPs {
		ips = append(ips, ip)
	}

	return &TritonInstance{
		ID:   instance.ID,
		Name: instance.Name,
		IPs:  ips,
		Tags: instance.Tags,
	}, nil
}
