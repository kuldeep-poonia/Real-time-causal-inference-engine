# ABSIA - Causal Intelligence Engine

ABSIA is an observability control plane for distributed systems. It watches service metrics, builds a causal view of the system, identifies likely root causes, explains the evidence, and blocks unsafe automated actions when confidence is too low.

ABSIA is meant to run next to your application. A user should be able to add the ABSIA Docker image to their stack, expose the UI port, connect metrics, and start using the control plane.

## What ABSIA Does

ABSIA answers three operator questions:

1. Which service is most likely causing the incident?
2. What evidence supports that causal diagnosis?
3. Is it safe to recommend or run an intervention?

The pipeline has five phases:

1. Signal physics: arrival rate, service rate, queue depth, load.
2. Pattern intelligence: spikes, drift, divergence, regime changes.
3. Causal graph: cause/effect structure across nodes.
4. Explanation: causal effects, uncertainty, root-cause narrative.
5. Policy and safety: ranked interventions guarded by confidence checks.

If ABSIA is not confident, it returns `UNKNOWN` instead of pretending.

## Quick Start

### Run ABSIA With Docker Compose

```bash
docker compose up --build
```

Open the UI:

```text
http://localhost:8080
```

The UI is embedded in the ABSIA binary. There is no separate frontend build step.

### Build Only The ABSIA Image

```bash
docker build -t absia:latest .
```

Run it:

```bash
docker run --rm -p 8080:8080 absia:latest
```

Open:

```text
http://localhost:8080
```

## Use ABSIA In Another App

Add ABSIA to your application's `docker-compose.yml`:

```yaml
services:
  absia:
    image: absia:latest
    container_name: absia
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      PORT: "8080"
      ABSIA_LOG_LEVEL: info
      ABSIA_POLICY_STORE_PATH: /data/policies
      # Optional but recommended:
      PROMETHEUS_URL: http://prometheus:9090
      # Optional security for /act:
      ABSIA_API_KEY: "${ABSIA_API_KEY:-}"
    volumes:
      - absia_policies:/data/policies

volumes:
  absia_policies:
```

Then:

```bash
docker compose up --build
```

Open the UI on the published port:

```text
http://localhost:8080
```

ABSIA starts immediately. It will show nodes after metrics arrive from Prometheus or from direct `/ingest` calls.

## Connecting Metrics

ABSIA supports two integration paths.

### Option 1: Prometheus

Set:

```yaml
environment:
  PROMETHEUS_URL: http://prometheus:9090
```

ABSIA polls Prometheus, maps container/service signals into queueing metrics, stores them, and the UI discovers nodes automatically.

The included `docker-compose.yml` starts ABSIA with Prometheus, cAdvisor, and node-exporter:

```bash
docker compose up --build
```

UI:

```text
http://localhost:8080
```

Prometheus:

```text
http://localhost:9091
```

### Option 2: Push Metrics From Your App

Any service can push samples to ABSIA:

```bash
curl -X POST http://localhost:8080/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "node_id": "orders-api",
    "arrival_rate": 120,
    "service_rate": 95,
    "queue_length": 18
  }'
```

Fields:

| Field | Meaning |
|---|---|
| `node_id` | Service, worker, queue, database, or dependency name. |
| `arrival_rate` | Work arriving per second. |
| `service_rate` | Work the node can process per second. Must be greater than 0. |
| `queue_length` | Current queued work. |

After a node has at least 4 samples, ABSIA treats it as real data for analysis.

## API

### `GET /health`

Returns liveness, readiness, version, auth mode, and whether real data is available.

```bash
curl http://localhost:8080/health
```

### `GET /nodes`

Returns the current node inventory used by the UI.

```bash
curl http://localhost:8080/nodes
```

Example:

```json
{
  "success": true,
  "real_data_available": true,
  "node_count": 2,
  "nodes": [
    {
      "node_id": "orders-api",
      "arrival_rate": 120,
      "service_rate": 95,
      "queue_length": 18,
      "load": 1.263,
      "sample_count": 8,
      "pipeline_ready": true,
      "status": "overloaded"
    }
  ]
}
```

### `POST /ingest`

Stores one metrics sample.

```bash
curl -X POST http://localhost:8080/ingest \
  -H "Content-Type: application/json" \
  -d '{"node_id":"api","arrival_rate":80,"service_rate":100,"queue_length":6}'
```

### `POST /analyze`

Runs the full causal pipeline.

```bash
curl -X POST http://localhost:8080/analyze \
  -H "Content-Type: application/json" \
  -d '{"node_id":"api","arrival_rate":80,"service_rate":100,"queue_length":6}'
```

### `POST /explain`

Runs the causal explanation path.

```bash
curl -X POST http://localhost:8080/explain \
  -H "Content-Type: application/json" \
  -d '{"node_id":"api","arrival_rate":80,"service_rate":100,"queue_length":6}'
```

### `POST /act`

Returns ranked intervention recommendations. If `ABSIA_API_KEY` is set, include a bearer token.

```bash
curl -X POST http://localhost:8080/act \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ABSIA_API_KEY" \
  -d '{"node_id":"api","arrival_rate":80,"service_rate":100,"queue_length":6}'
```

When safety confidence is too low, `/act` returns HTTP `503` and no actions.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port inside the container. |
| `ABSIA_API_KEY` | empty | Bearer token for `/act`. Empty means auth disabled. |
| `PROMETHEUS_URL` | empty | Prometheus base URL. |
| `PROMETHEUS_QUERY` | `rate(container_cpu_usage_seconds_total[1m])` | Optional PromQL query. |
| `ABSIA_POLICY_STORE_PATH` | `/data/policies` | Directory for persisted policy weights. |
| `ABSIA_SEED` | `42` | Deterministic seed. |
| `ABSIA_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error`. |
| `ABSIA_RATE_LIMIT_RPS` | `10` | Per-IP request rate limit. |
| `ABSIA_RATE_LIMIT_BURST` | `20` | Rate-limit burst size. |
| `ABSIA_MAX_BODY_BYTES` | `1048576` | Max JSON request size. |
| `ABSIA_READ_TIMEOUT_SECONDS` | `5` | HTTP read timeout. |
| `ABSIA_WRITE_TIMEOUT_SECONDS` | `30` | HTTP write timeout. |
| `ABSIA_IDLE_TIMEOUT_SECONDS` | `120` | HTTP idle timeout. |

## Development

```bash
go build ./...
go test ./...
go run .
```

The UI is served from:

```text
pkg/api/ui/absia.html
```

The API embeds that file into the Go binary.

## Security Notes

- Set `ABSIA_API_KEY` in production so `/act` requires a bearer token.
- Keep `/act` behind trusted network boundaries if actions will be automated.
- `GET /health`, `GET /nodes`, `POST /ingest`, `POST /analyze`, and `POST /explain` are unauthenticated by default.
- Request bodies are size-limited.
- ABSIA only calls Prometheus when `PROMETHEUS_URL` is configured.
