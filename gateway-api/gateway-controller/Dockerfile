# Build Stage
FROM golang:1.24 as builder

# Set necessary environment variables
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64

# Create working directory inside the container
WORKDIR /workspace

COPY . .

# Clone golang.org/x/crypto and apply patch 599995
# This allows using hostnames as remote addresses when doing ssh
# forwarding
RUN git clone --branch v0.37.0 https://go.googlesource.com/crypto /tmp/crypto && \
    cd /tmp/crypto && \
    git config user.email "builder@local" && \
    git config user.name "Builder" && \
    git fetch https://go.googlesource.com/crypto refs/changes/95/599995/1 && \
    git cherry-pick FETCH_HEAD

# Download dependencies early to cache them
RUN go mod download

# Replace golang.org/x/crypto with our patched local version
RUN echo "replace golang.org/x/crypto => /tmp/crypto" >> go.mod

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