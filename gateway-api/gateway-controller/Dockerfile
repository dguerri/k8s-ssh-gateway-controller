# Build Stage
FROM golang:1.24 as builder

# Set necessary environment variables
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# Create working directory inside the container
WORKDIR /workspace

COPY . .

# Download dependencies early to cache them
RUN go mod download

# Build the controller binary
RUN go build -a -o gateway-controller main.go

# Final Minimal Stage
FROM gcr.io/distroless/static:nonroot
LABEL name=pico-sh-gateway-api-controller
LABEL version=1.0

# Set working directory
WORKDIR /

# Copy the built binary from the builder stage
COPY --from=builder /workspace/gateway-controller .

# Use non-root user for security best practices
USER nonroot:nonroot

# Command to run the controller
ENTRYPOINT ["/gateway-controller"]