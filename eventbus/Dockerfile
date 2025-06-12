# Stage 1: Builder
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build tools if necessary (e.g., for CGO or specific build scripts)
# RUN apk add --no-cache gcc musl-de

# Copy go.mod and go.sum first to leverage Docker layer caching for dependencies
COPY go.mod go.sum ./app/

# Copy the entire application source code
COPY . /app/eventbus

WORKDIR /app/eventbus

RUN ls
RUN go mod tidy
RUN go mod download && go mod verify

# Build the application.
# CGO_ENABLED=0 creates a statically linked binary, essential for distroless/static images.
# -ldflags="-w -s" strips debug information and symbol table, reducing binary size.
# -a forces rebuilding of packages that are already up-to-date.
# -installsuffix cgo ensures a clean build if CGO settings change.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo -ldflags="-w -s" -o /app/eventbus/eventbus

# Stage 2: Final Runtime Image
# Use a minimal base image. gcr.io/distroless/static-debian11 is excellent for static Go binaries.
FROM gcr.io/distroless/static-debian12

# For applications that need CA certificates (e.g., for making HTTPS calls to external services)
# you might need a base image that includes them, or copy them in.
# 'distroless/static' does not include them. 'distroless/base' or 'alpine' do.
# If using alpine:
# FROM alpine:latest
# RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /app/eventbus /app/eventbus

# Expose the port the application listens on (must match the application's configuration)
EXPOSE 8080

# Set a non-root user for security.
# distroless/static runs as nonroot:nonroot (user 65532) by default.
# If using a different base image like alpine, you might need:
# RUN addgroup -S appgroup && adduser -S appuser -G appgroup
# USER appuser

# Define the entrypoint for the container
ENTRYPOINT ["/app/eventbus/eventbus"]