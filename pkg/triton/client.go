package triton

import (
	"context"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"net/http"
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
	// Read the SSH private key file
	privateKeyData, err := ioutil.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %v", err)
	}

	// Parse the private key
	block, _ := pem.Decode(privateKeyData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block containing private key")
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
	_, err = computeClient.Instances().List(context.Background(), &compute.ListInstancesInput{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Triton API: %v", err)
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
	metadata := map[string]string{
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

	// Use Triton API to create the load balancer as a machine
	// For demo: using a fixed package and image ID
	// In production, these should be configurable
	createInput := &compute.CreateInstanceInput{
		Name:     params.Name,
		Package:  "g4-highcpu-1G", // Example package, should be configurable
		Image:    "70e3ae72-96b6-11ea-9274-2f3c66e8b2c4", // Example image ID for HAProxy, should be configurable
		Metadata: metadata,
		Tags: map[string]string{
			"k8s-service":  params.Name,
			"managed-by":   "triton-loadbalancer-controller",
			"loadbalancer": "true",
		},
	}

	instance, err := c.compute.Instances().Create(ctx, createInput)
	if err != nil {
		return err
	}

	// Wait for the instance to be provisioned
	err = c.compute.Instances().WaitForState(ctx, &compute.WaitForStateInput{
		ID:        instance.ID,
		State:     "running",
		Timeout:   300, // 5 minutes timeout
	})
	if err != nil {
		return err
	}

	return nil
}

// DeleteLoadBalancer deletes a load balancer in Triton
func (c *Client) DeleteLoadBalancer(ctx context.Context, name string) error {
	// Find instance by name
	listInput := &compute.ListInstancesInput{
		Name: name,
		Tags: map[string]string{
			"loadbalancer": "true",
			"managed-by":   "triton-loadbalancer-controller",
		},
	}
	
	instances, err := c.compute.Instances().List(ctx, listInput)
	if err != nil {
		return err
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
		return err
	}
	
	// Wait for the instance to be deleted (no longer appears in list)
	for i := 0; i < 30; i++ { // Retry for a maximum of 30 times (5 minutes)
		instances, err := c.compute.Instances().List(ctx, listInput)
		if err != nil {
			return err
		}
		
		if len(instances) == 0 {
			// Instance successfully deleted
			return nil
		}
		
		// Sleep for 10 seconds before retrying
		time.Sleep(10 * time.Second)
	}
	
	return fmt.Errorf("timed out waiting for load balancer %s to be deleted", name)
}

// UpdateLoadBalancer updates an existing load balancer in Triton
func (c *Client) UpdateLoadBalancer(ctx context.Context, name string, params LoadBalancerParams) error {
	// Find instance by name
	listInput := &compute.ListInstancesInput{
		Name: name,
		Tags: map[string]string{
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
	metadata := map[string]string{
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
		Tags: map[string]string{
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
	if portmapStr, ok := instance.Metadata["cloud.tritoncompute:portmap"]; ok {
		// Parse portmap string
		portMappings := parsePortMap(portmapStr)
		params.PortMappings = portMappings
	}
	
	if maxRSStr, ok := instance.Metadata["cloud.tritoncompute:max_rs"]; ok {
		if maxRS, err := strconv.Atoi(maxRSStr); err == nil {
			params.MaxBackends = maxRS
		}
	}
	
	if certName, ok := instance.Metadata["cloud.tritoncompute:certificate_name"]; ok {
		params.CertificateName = certName
	}
	
	if metricsACL, ok := instance.Metadata["cloud.tritoncompute:metrics_acl"]; ok {
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

// parsePortMap parses a port map string into PortMapping structs
func parsePortMap(portmapStr string) []PortMapping {
	var mappings []PortMapping
	
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
	Tags map[string]string
}

// GetInstanceByName retrieves a Triton instance by name
func (c *Client) GetInstanceByName(ctx context.Context, name string) (*TritonInstance, error) {
	// Find instance by name and tags
	listInput := &compute.ListInstancesInput{
		Name: name,
		Tags: map[string]string{
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