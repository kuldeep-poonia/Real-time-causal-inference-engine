# ABSIA — Causal Intelligence Engine

> **A**daptive **B**ayes-Structural **I**nference & **A**ction engine for distributed systems observability.  
> Root-cause analysis that knows when it doesn't know — and tells you why.

---

## What is ABSIA?

Most monitoring tools tell you *that* something is wrong. ABSIA tells you *why* — and gives you the mathematical evidence to back it up.

When your microservices are struggling — queues backing up, latency spiking, throughput dropping — ABSIA runs a five-phase causal pipeline to identify which node is the true root cause, separate it from correlated-but-innocent bystanders, and recommend targeted interventions. Then it gates those recommendations behind an epistemic safety layer that refuses to guess when the evidence is too weak.

**If ABSIA isn't confident, it says UNKNOWN instead of lying to you.**

---

## The Simple Explanation

Imagine three services in a chain: `auth → api → database`. The database starts slowing down. The API latency goes up. The auth service backs up. Most tools alarm on all three equally.

ABSIA:
1. Measures the queueing physics of each service (arrival rate, processing rate, queue depth)
2. Detects which signals are changing and in what pattern
3. Builds a causal graph — a map of *cause and effect*, not just correlation
4. Uses Pearl's do-calculus to test whether intervening on each node would actually fix the problem
5. Runs a reinforcement learning policy to rank interventions by expected causal impact
6. Returns `database` as the root cause with a confidence score, backdoor-adjusted effect size, and a specific intervention recommendation

If the graph is too sparse, or the latent confounders are too strong, it returns `UNKNOWN` with probing recommendations — specific nodes to instrument next.

---

## Quick Start

### Requirements

- Go 1.22+
- Docker + Docker Compose (optional but recommended)
- A Prometheus endpoint (optional — ABSIA works without one using its built-in synthetic data engine)

### Run with Docker

```bash
git clone <your-repo>
cd absia
docker-compose up --build
```

Open your browser at **http://localhost:8080** — the dashboard loads automatically.

### Run locally

```bash
cp .env.example .env
# Edit .env if needed (all defaults work out of the box)
go run .
```

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `ABSIA_API_KEY` | *(empty)* | Bearer token for `/act`. Unset = auth disabled |
| `PROMETHEUS_URL` | *(empty)* | Prometheus base URL. Unset = synthetic data mode |
| `ABSIA_SEED` | `42` | RNG seed for reproducibility |
| `ABSIA_RATE_LIMIT_RPS` | `10` | Per-IP rate limit (requests/second) |
| `ABSIA_RATE_LIMIT_BURST` | `20` | Per-IP burst capacity |
| `ABSIA_MAX_BODY_BYTES` | `65536` | Max request body size |
| `ABSIA_POLICY_STORE_PATH` | `/data/policies` | Directory for persisting RL policy weights |
| `ABSIA_LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `ABSIA_READ_TIMEOUT` | `15` | HTTP read timeout (seconds) |
| `ABSIA_WRITE_TIMEOUT` | `60` | HTTP write timeout (seconds) |
| `ABSIA_IDLE_TIMEOUT` | `120` | HTTP idle timeout (seconds) |
| `ABSIA_PROCESSOR_ALPHA` | `0.3` | EMA smoothing factor for signal processing |

---

## API Reference

All requests/responses use `Content-Type: application/json`.

### `GET /health`

Liveness probe. No authentication required. Never rate-limited.

**Response:**
```json
{
  "status": "ok",
  "version": "2.1.0",
  "ready": true,
  "real_data_available": false,
  "ingested_node_count": 3,
  "auth_mode": "disabled"
}
```

| Field | Description |
|---|---|
| `ready` | `true` when the pipeline can process requests |
| `real_data_available` | `true` when Prometheus data or ≥4 ingested samples are available |
| `ingested_node_count` | Number of distinct node IDs with stored samples |
| `auth_mode` | `"enabled"` or `"disabled"` — affects `/act` |

---

### `POST /ingest`

Store a metrics sample for a node. Samples accumulate in a sliding window (default: 60 samples). Once a node has ≥4 samples, the pipeline will use real data instead of synthetic.

**Request:**
```json
{
  "node_id": "api-gateway",
  "arrival_rate": 120.5,
  "service_rate": 95.0,
  "queue_length": 18
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `node_id` | string | no | Node identifier. Defaults to `"primary"` |
| `arrival_rate` | float | **yes** | Request/task arrival rate (λ), must be ≥ 0 |
| `service_rate` | float | **yes** | Processing capacity (μ), must be > 0 |
| `queue_length` | int | **yes** | Current queue depth, must be ≥ 0 |

**Response:**
```json
{
  "success": true,
  "sample_count": 7,
  "node_id": "api-gateway",
  "pipeline_ready": true
}
```

---

### `POST /analyze`

Run all five pipeline phases. Returns root-cause identification, causal effect magnitudes, backdoor-adjusted effects, and safety gate result.

**Request:** same schema as `/ingest`

**Response:**
```json
{
  "success": true,
  "root_cause": "database",
  "physics_root_cause": "database",
  "causes": {
    "database": 0.812,
    "api-gateway": 0.234
  },
  "backdoor_effects": {
    "database": 0.791,
    "api-gateway": 0.198
  },
  "patterns_detected": 3,
  "actions_recommended": 2,
  "data_source": "real",
  "execution_time_ms": 142.7,
  "safety": {
    "confidence_state": "CONFIRMED",
    "confidence_score": 0.83,
    "latent_risk": "LOW",
    "graph_coverage": 0.91,
    "determinism": 0.87,
    "fallback_triggered": false,
    "fallback_reasons": []
  }
}
```

**Safety states:**

| State | Meaning | HTTP Status |
|---|---|---|
| `CONFIRMED` | Score ≥ 0.75. High-confidence causal identification. | 200 |
| `PROBABLE` | Score 0.45–0.75. Plausible but uncertain. | 200 |
| `UNKNOWN` | Score < 0.45 or latent risk HIGH. Do not act automatically. | 200 |

---

### `POST /explain`

Run phases 1–4 only. Returns the causal explanation without the RL policy layer.

**Request:** same schema as `/ingest`

**Response:**
```json
{
  "success": true,
  "root_cause": "database",
  "causes": ["database", "api-gateway"],
  "effects": {
    "database": 0.812,
    "api-gateway": 0.234
  },
  "data_source": "synthetic",
  "execution_time_ms": 88.3,
  "safety": { ... }
}
```

---

### `POST /act` *(auth required)*

Run the full pipeline including the RL policy layer. Returns ranked intervention recommendations. Requires `Authorization: Bearer <your-key>` if `ABSIA_API_KEY` is set.

Returns **HTTP 503** (not 400 or 500) when the safety gate is `UNKNOWN` — this signals a retriable condition requiring human review, not a permanent error.

**Request:** same schema as `/ingest`

**Response (safe to act):**
```json
{
  "success": true,
  "recommended_actions": [
    {
      "policy_rank": 1,
      "policy_score": 0.847,
      "interventions": {
        "database": 0.45
      }
    },
    {
      "policy_rank": 2,
      "policy_score": 0.621,
      "interventions": {
        "api-gateway": 0.72
      }
    }
  ],
  "policy_trained": true,
  "data_source": "real",
  "execution_time_ms": 187.4,
  "safety": { ... }
}
```

**Response (safety gate UNKNOWN — HTTP 503):**
```json
{
  "success": false,
  "safety_blocked": true,
  "confidence_state": "UNKNOWN",
  "probe_recommendations": ["cache", "worker-pool"],
  "message": "Safety gate UNKNOWN: automated action blocked. Instrument probe nodes and retry."
}
```

---

## Error Responses

All errors share this shape:

```json
{
  "success": false,
  "errors": ["arrival_rate must be >= 0", "service_rate must be > 0"]
}
```

| HTTP Status | Meaning |
|---|---|
| 400 | Invalid JSON body |
| 401 | Missing or invalid `Authorization` header on `/act` |
| 405 | Wrong HTTP method |
| 413 | Request body too large |
| 422 | Validation failed (field errors in `errors` array) |
| 429 | Rate limit exceeded |
| 503 | Safety gate UNKNOWN (from `/act` only) |

---

## Connecting Prometheus

Set `PROMETHEUS_URL=http://your-prometheus:9090` and ABSIA will poll the following metrics every 15 seconds:

```
container_cpu_usage_seconds_total
container_memory_usage_bytes
http_requests_total
http_request_duration_seconds
```

Each scraped node is stored in the metrics store and treated as real data. The dashboard's data-mode badge switches from `SYNTHETIC` to `REAL` automatically.

---

## Architecture Overview

```
Prometheus ──►┐
/ingest  ─────┼──► Metrics Store ──► Phase 1: Signal Physics
              │                            │
              └──────────────────────────► Phase 2: Pattern Detection
                                                │
                                          Phase 3: Causal Graph
                                          (do-calculus + d-separation)
                                                │
                                          Phase 4: Explanation
                                                │
                                          Phase 5: RL Policy + Safety Gate
                                                │
                               ┌────────────────┴──────────────┐
                           CONFIRMED / PROBABLE            UNKNOWN
                               │                               │
                        /analyze, /explain, /act           HTTP 503
                                                     (probe recommendations)
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the full technical deep-dive.

---

## Running the Dashboard

The dashboard is embedded directly into the binary — no separate frontend build step required. Just start the server and open `http://localhost:8080`.

**Dashboard sections:**
- **Dashboard** — live health status, quick-run panel, endpoint reference
- **Run Pipeline** — full pipeline execution with phase animation and results
- **Ingest Data** — manual sample ingestion with utilization tracking (ρ = λ/μ)
- **Monitor** — auto-refreshing health metrics with history timeline

---

## Development

```bash
# Verify the project builds
go build ./...

# Run all tests
go test ./...

# Run with verbose pipeline logs
ABSIA_LOG_LEVEL=debug go run .

# Build Docker image
docker build -t absia:latest .

# Full stack with Docker Compose
docker-compose up --build
```

---

## Security Notes

- **`/act` is the only privileged endpoint.** Set `ABSIA_API_KEY` in production.
- The per-IP rate limiter is enabled by default. Set `ABSIA_RATE_LIMIT_RPS=0` to disable in trusted environments.
- Request bodies are capped at `ABSIA_MAX_BODY_BYTES` (default 64 KB) before the first byte is parsed.
- ABSIA makes no outbound connections except to `PROMETHEUS_URL` if configured. It has zero external Go dependencies.

---

## License

See [LICENSE](LICENSE).
