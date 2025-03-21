# Build stage
FROM golang:1.21 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# Cache dependencies before building and copying source
RUN go mod download

# Copy the source code
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY internal/ internal/

# Build with CGO_ENABLED=0 for a completely static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager cmd/manager/main.go

# Use distroless as minimal base image
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]