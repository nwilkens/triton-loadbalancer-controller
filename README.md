# Triton LoadBalancer Controller

A Kubernetes controller that automatically provisions and configures HAProxy-based load balancers in Triton Data Center when Kubernetes Services of type LoadBalancer are created.

## Overview

The Triton LoadBalancer Controller watches for Kubernetes Service resources of type LoadBalancer and creates corresponding load balancer instances in Triton Data Center using the CloudAPI. The controller manages the full lifecycle of these load balancers, including creation, updates, and deletion.

When a Service of type LoadBalancer is created or updated, the controller:

1. Automatically provisions a load balancer instance in Triton with the appropriate configuration
2. Sets up the necessary port mappings based on the Service ports
3. Configures certificates for HTTPS if specified
4. Sets up metrics access control if configured
5. Updates the Service status with the load balancer's IP address

## Features

- **Automatic Load Balancer Provisioning**: Creates HAProxy-based load balancers in Triton when a LoadBalancer type Service is created in Kubernetes
- **Dynamic Port Mapping**: Maps Service ports to the load balancer configuration
- **HTTPS Support**: Integration with triton-dehydrated for certificate generation
- **Metrics Endpoint**: Optional metrics endpoint with IP-based access control
- **Full Lifecycle Management**: Handles creation, updates, and deletion of load balancers

## Prerequisites

- Kubernetes cluster v1.19+
- Access to Triton Data Center with valid credentials
- Proper RBAC permissions to watch and modify Services in the cluster

## Installation

1. Clone this repository:
   ```
   git clone https://github.com/triton/loadbalancer-controller.git
   cd loadbalancer-controller
   ```

2. Edit the credentials in `config/controller.yaml` to include your Triton account details:
   ```yaml
   stringData:
     triton-url: "https://us-east-1.api.joyent.com"  # Replace with your Triton CloudAPI endpoint
     triton-account: ""                              # Replace with your Triton account ID
     triton-key-id: ""                               # Replace with your Triton key ID (fingerprint)
     triton-key: |                                   # MUST BE PEM FORMAT: $ ssh-keygen -p -m PEM -f <id_rsa_file> to convert a file to PEM
       -----BEGIN RSA PRIVATE KEY-----
       ...
       -----END RSA PRIVATE KEY-----
   ```
   
   Note: If your SSH key is not in PEM format, convert it using:
   ```
   ssh-keygen -p -m PEM -f your_private_key_file
   ```

3. Apply the controller configuration:
   ```
   kubectl apply -f config/controller.yaml
   ```

## Usage

### Creating a LoadBalancer Service

To create a load balancer, simply create a Kubernetes Service of type LoadBalancer:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-service
  annotations:
    cloud.tritoncompute/max_rs: "64"  # Optional: Set maximum number of backends
    cloud.tritoncompute/certificate_name: "example.com"  # Optional: Certificate subject
    cloud.tritoncompute/metrics_acl: "10.0.0.0/8 192.168.0.0/16"  # Optional: Metrics access control
spec:
  type: LoadBalancer
  ports:
  - name: http
    port: 80
    targetPort: 8080
  - name: https
    port: 443
    targetPort: 8443
  selector:
    app: my-app
```

### Annotations

The controller recognizes several annotations that can be used to configure the load balancer:

- `cloud.tritoncompute/max_rs`: Optional; maximum number of backends (default: 32)
- `cloud.tritoncompute/certificate_name`: Optional; comma-separated list of certificate subjects
- `cloud.tritoncompute/metrics_acl`: Optional; IP prefix or comma/space-separated list of prefixes for metrics access control

### Port Mapping

The controller automatically maps the Service ports to the load balancer configuration:

- Ports with name "http" or port 80 are configured as HTTP
- Ports with name "https" or port 443 are configured as HTTPS
- All other ports are configured as TCP

## Building from Source

1. Build the controller binary:
   ```
   go build -o manager cmd/manager/main.go
   ```

2. Build the Docker image:
   ```
   docker build -t triton/loadbalancer-controller:latest .
   ```

3. Push the image to a registry:
   ```
   docker push triton/loadbalancer-controller:latest
   ```

## Development

### Project Structure

- `/cmd/manager`: Main entry point for the controller
- `/pkg/controller`: Controller logic for reconciling Services
- `/pkg/triton`: Triton CloudAPI client implementation
- `/config`: Kubernetes manifests for deploying the controller

### Adding Features

1. Clone the repository
2. Create a new branch for your feature
3. Implement the feature or fix
4. Add tests for your changes
5. Submit a pull request

## Troubleshooting

### Common Issues

- **Load balancer not being created**: Verify that the Triton credentials are correct and that the controller has the necessary RBAC permissions
- **Load balancer status not being updated**: Check the controller logs for any errors communicating with the Triton API
- **HTTPS not working**: Ensure that the certificate name is correctly specified and that the triton-dehydrated service is running properly

### Viewing Logs

```
kubectl logs -n triton-system -l app=triton-loadbalancer-controller
```

## License

MIT License