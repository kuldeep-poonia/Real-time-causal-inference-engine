


FROM golang:1.23-alpine AS builder

WORKDIR /build

# Copy module definition first for layer-cache efficiency.
# go.mod declares zero external dependencies — no `go mod download` needed.
COPY go.mod ./

# Copy source tree.
COPY . .

# Build a fully static binary.
# CGO_ENABLED=0  → no libc dependency in the final image.
# -trimpath      → strips local filesystem paths from stack traces.
# -ldflags=-s -w → strips symbol table and DWARF debug info (~30% smaller binary).
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o absia ./


FROM scratch

# CA certificates are needed for any future HTTPS egress (Prometheus scrape,
# optional webhook notifications). Zero cost to include; painful to add later.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the compiled binary.
COPY --from=builder /build/absia /absia

# /data/policies is the only persistent directory.
# Metrics are transient (rebuilt every 15s from Docker stats).
VOLUME ["/data/policies"]

# Default HTTP port.
EXPOSE 8080


# All of these can be overridden via docker run -e or docker-compose environment:.
ENV ABSIA_POLICY_STORE_PATH=/data/policies \
    ABSIA_LOG_LEVEL=info \
    ABSIA_RATE_LIMIT_RPS=10 \
    ABSIA_RATE_LIMIT_BURST=20

ENTRYPOINT ["/absia"]