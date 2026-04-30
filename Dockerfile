# syntax=docker/dockerfile:1

# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Copy module files first for layer-cache efficiency.
COPY go.mod ./

# No go.sum needed (zero external dependencies — stdlib only).
# Download nothing; just prepare the module.

COPY . .

# Build a fully static binary. CGO is disabled so the final image needs no
# libc. -trimpath removes local filesystem paths from stack traces.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o absia ./

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM scratch

# Import CA certificates for HTTPS calls to Prometheus.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the compiled binary.
COPY --from=builder /build/absia /absia

# Policy weights directory — override via ABSIA_POLICY_STORE_PATH.
VOLUME ["/data/policies"]

# Default HTTP port — override via PORT env var.
EXPOSE 8080

# Environment variable documentation (all have safe defaults):
#
#   ABSIA_API_KEY              Bearer token for /act endpoint (empty = disabled)
#   ABSIA_POLICY_STORE_PATH    Directory for persisted RL policy weights
#   ABSIA_SEED                 Base RNG seed (default: 42)
#   ABSIA_LOG_LEVEL            debug | info | warn | error (default: info)
#   ABSIA_MAX_BODY_BYTES       Max request body size (default: 1048576)
#   ABSIA_READ_TIMEOUT_SECONDS HTTP read timeout (default: 5)
#   ABSIA_WRITE_TIMEOUT_SECONDS HTTP write timeout (default: 30)
#   ABSIA_IDLE_TIMEOUT_SECONDS  HTTP idle timeout (default: 120)
#   PROMETHEUS_URL             Prometheus base URL (optional)
#   PROMETHEUS_QUERY           PromQL query for metric ingestion
#   PORT                       HTTP listen port (default: 8080)

ENV ABSIA_POLICY_STORE_PATH=/data/policies
ENV ABSIA_LOG_LEVEL=info

ENTRYPOINT ["/absia"]
