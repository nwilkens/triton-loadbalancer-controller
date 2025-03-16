# Image URL to use all building/pushing image targets
IMG ?= triton/loadbalancer-controller:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifneq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOBIN)
else
GOBIN=$(shell go env GOPATH)/bin
endif

all: build

# Run tests
test: fmt vet
	go test ./... -v

# Build the binary
build: fmt vet
	go build -o bin/manager cmd/manager/main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: fmt vet
	go run ./cmd/manager/main.go

# Run go fmt against code
fmt:
	go fmt ./...

# Run go vet against code
vet:
	go vet ./...

# Build the docker image
docker-build:
	docker build . -t ${IMG}

# Push the docker image
docker-push:
	docker push ${IMG}

# Deploy to Kubernetes cluster
deploy: docker-build docker-push
	kubectl apply -f config/controller.yaml

# Generate manifests
manifests:
	mkdir -p config/generated
	# Add manifest generation commands here if needed
