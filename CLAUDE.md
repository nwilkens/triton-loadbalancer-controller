# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Common Development Commands

### Build and Test
- `make build` - Build the controller binary
- `make test` - Run unit tests with formatting and vetting
- `make fmt` - Format Go code
- `make vet` - Run Go vet for static analysis
- `go test ./...` - Run all unit tests
- `TRITON_TEST_INTEGRATION=true go test -tags=integration ./...` - Run integration tests (requires Triton credentials)

### Running Locally
- `make run` - Run the controller locally (requires kubeconfig and Triton credentials)
- Test script for manual testing:
  ```bash
  go run bin/test-loadbalancer.go --key-path=/path/to/key --key-id=<id> --account=<account> --url=<url> --name=test-lb --action=create
  ```

### Docker and Deployment
- `make docker-build` - Build Docker image
- `make docker-push` - Push Docker image to registry
- `make deploy` - Deploy to Kubernetes cluster (ensure config/controller.yaml has correct credentials)

### Linting and Security
- `go fmt ./...` - Format all Go files
- `go vet ./...` - Run Go static analysis
- `golangci-lint run` - Run comprehensive linting (if installed)

## Architecture Overview

### Core Components

1. **Controller** (`pkg/controller/loadbalancer_controller.go`)
   - Implements Kubernetes controller-runtime reconciliation pattern
   - Watches Services of type LoadBalancer
   - Manages full lifecycle: creation, updates, deletion
   - Maps Service ports to HAProxy configuration

2. **Triton Client** (`pkg/triton/client.go`)
   - Wraps triton-go/v2 CloudAPI client
   - Interface-based design for testability
   - Handles load balancer provisioning and configuration
   - Manages instance lifecycle in Triton Data Center

3. **Main Entry Point** (`cmd/manager/main.go`)
   - Sets up controller-runtime manager
   - Configures Kubernetes client and informers
   - Initializes Triton client with credentials

### Key Design Patterns

- **Interface-Based Design**: `TritonClientInterface` enables easy mocking and testing
- **Reconciliation Loop**: Standard Kubernetes controller pattern with proper error handling
- **Structured Logging**: Uses controller-runtime logging throughout
- **Configuration via Environment**: Customizable behavior through environment variables

### Service Port Mapping Logic
- Port name "http" or port 80 → HTTP backend
- Port name "https" or port 443 → HTTPS backend with certificate support
- All other ports → TCP passthrough

### Annotations Processing
- `cloud.tritoncompute/max_rs` - Maximum backend connections
- `cloud.tritoncompute/certificate_name` - Certificate subjects (comma-separated)
- `cloud.tritoncompute/metrics_acl` - Metrics endpoint access control

## Testing Strategy

### Unit Tests
- Mock Triton client for controller tests
- Test reconciliation logic with various Service configurations
- Verify error handling and edge cases

### Integration Tests
- Real Triton environment required
- Tests full load balancer lifecycle
- Cleanup after test completion
- Run with: `TRITON_TEST_INTEGRATION=true go test -tags=integration ./...`

## Important Implementation Details

### SSH Key Format
- Triton requires PEM format SSH keys
- Convert existing keys: `ssh-keygen -p -m PEM -f <key_file>`
- Key is stored in Kubernetes Secret

### Timeouts
- Provisioning timeout: 300s (configurable via `TRITON_PROVISION_TIMEOUT`)
- Deletion timeout: 300s (configurable via `TRITON_DELETE_TIMEOUT`)
- Controller uses exponential backoff for retries

### Security Considerations
- Controller runs as non-root user (65532:65532)
- Uses distroless base image
- Minimal RBAC permissions (Services only)
- SSH key stored securely in Kubernetes Secret

### Error Handling
- All errors include context for debugging
- Transient errors trigger reconciliation retry
- Permanent errors update Service events

## Environment Variables

- `TRITON_LB_PACKAGE` - Instance package size (default: g4-highcpu-1G)
- `TRITON_LB_IMAGE` - HAProxy image ID (required)
- `TRITON_PROVISION_TIMEOUT` - Provisioning timeout in seconds
- `TRITON_DELETE_TIMEOUT` - Deletion timeout in seconds