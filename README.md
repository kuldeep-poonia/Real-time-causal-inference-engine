# ABSIA — Autonomous Bayesian System Intelligence Agent

**Causal intelligence for distributed systems. Drop it into your Docker stack. It figures out the rest.**

ABSIA watches your containers, builds a causal model of how they affect each other, identifies the most likely root cause when something goes wrong, and tells you what to do about it — with a safety gate that says *"I don't know"* instead of guessing when confidence is too low.

---

## How It Works

Most observability tools show you *what* is happening. ABSIA answers *why*.

It runs a five-phase intelligence pipeline on every analysis request:

| Phase | What It Does |
|---|---|
| **Signal Physics** | Reads CPU/memory from your containers and maps them to M/M/1 queue metrics — arrival rate λ, service rate μ, and queue depth L |
| **Pattern Detection** | Detects spikes, drift, regime changes, and divergence across all nodes |
| **Causal Graph** | Builds a directed causal graph using Pearl's do-calculus, backdoor adjustment, and d-separation |
| **Explanation** | Produces a ranked, evidence-backed root-cause narrative with uncertainty quantification |
| **Policy + Safety Gate** | Recommends ranked interventions, then runs five independent safety checks — if any fail, it returns `UNKNOWN` instead of a bad recommendation |

The safety gate is not optional. ABSIA never surfaces a recommendation it isn't confident in.

---

## Quick Start

**Requirements: Docker. Nothing else.**

### One command

```bash
docker run -d \
  --name absia \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  yourdockerhub/absia:latest
```

Open **http://localhost:8080** — the dashboard appears immediately. ABSIA discovers every running container and starts collecting metrics automatically. No config files. No Prometheus setup. No environment variables required.

---

### With docker-compose (recommended if you have other services)

Create a `docker-compose.yml` in your project:

```yaml
services:
  absia:
    image: yourdockerhub/absia:latest
    ports:
      - "8080:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock

  # Your existing services — ABSIA discovers them automatically
  api:
    image: myorg/api:latest

  worker:
    image: myorg/worker:latest

  db:
    image: postgres:16-alpine
```

```bash
docker compose up -d
```

ABSIA polls every container's CPU and memory every 15 seconds. After four samples per container (~1 minute), the pipeline has enough data to run causal analysis. The dashboard updates live.

---

## What You See

Once running, open **http://localhost:8080**.

The dashboard shows:

- **Node inventory** — every discovered container with its current load status (`healthy` / `watch` / `pressure` / `overloaded`)
- **Real-time metrics** — arrival rate, service rate, queue depth, utilisation (ρ = λ/μ)
- **Causal analysis** — run on demand or continuously; shows the most probable root cause, causal chain, and confidence score
- **Safety status** — whether the pipeline is confident enough to recommend action, and why not if it isn't

---

## Connecting Your Own Metrics

If you want richer signals than CPU/memory (e.g. request rate, error rate, queue depth from your app), push them directly:

```bash
curl -X POST http://localhost:8080/ingest \
  -H "Content-Type: application/json" \
  -d '{
    "node_id": "orders-api",
    "arrival_rate": 340,
    "service_rate": 280,
    "queue_length": 42
  }'
```

| Field | Type | Description |
|---|---|---|
| `node_id` | string | Any name — service, queue, database, worker. Used as the causal graph node identifier. |
| `arrival_rate` | float | Work arriving per second (requests, messages, jobs). |
| `service_rate` | float | Work the node can process per second. Must be > 0. |
| `queue_length` | float | Current backlog depth. |

After **4 samples**, ABSIA treats that node as real data and includes it in causal analysis. Docker-discovered containers and manually pushed nodes coexist in the same pipeline.

---

## API Reference

All endpoints return JSON. All analysis responses include safety gate fields — `confidence_score`, `confidence_state`, `latent_risk`, `fallback_triggered` — regardless of which endpoint you call.

### `GET /health`

Liveness and readiness probe. Safe to use as a Docker healthcheck.

```bash
curl http://localhost:8080/health
```

```json
{
  "status": "ok",
  "ready": true,
  "version": "2.1.0",
  "real_data_available": true,
  "ingested_node_count": 4,
  "auth_mode": "disabled"
}
```

---

### `GET /nodes`

Current node inventory. Shows every container or pushed node with its latest metrics and status.

```bash
curl http://localhost:8080/nodes
```

```json
{
  "success": true,
  "real_data_available": true,
  "node_count": 3,
  "nodes": [
    {
      "node_id": "orders-api",
      "arrival_rate": 340,
      "service_rate": 280,
      "queue_length": 42,
      "load": 1.214,
      "sample_count": 12,
      "pipeline_ready": true,
      "status": "overloaded",
      "last_seen": "2025-05-23T10:14:00Z"
    },
    {
      "node_id": "payment-worker",
      "arrival_rate": 0.61,
      "service_rate": 1.0,
      "queue_length": 28.4,
      "load": 0.61,
      "sample_count": 12,
      "pipeline_ready": true,
      "status": "watch",
      "last_seen": "2025-05-23T10:14:00Z"
    }
  ]
}
```

**Status values:**

| Status | Load (ρ = λ/μ) | Meaning |
|---|---|---|
| `healthy` | < 0.60 | Normal operating range |
| `watch` | 0.60 – 0.84 | Elevated, monitor closely |
| `pressure` | 0.85 – 1.04 | High load, degradation likely |
| `overloaded` | ≥ 1.05 | Saturated, queueing unbounded |

---

### `POST /ingest`

Store one metrics sample for a node. No response body on success (HTTP 204).

```bash
curl -X POST http://localhost:8080/ingest \
  -H "Content-Type: application/json" \
  -d '{"node_id":"orders-api","arrival_rate":340,"service_rate":280,"queue_length":42}'
```

---

### `POST /analyze`

Run the full five-phase causal pipeline. Returns root cause, confidence, and ranked contributing factors.

```bash
curl -X POST http://localhost:8080/analyze \
  -H "Content-Type: application/json" \
  -d '{"node_id":"orders-api","arrival_rate":340,"service_rate":280,"queue_length":42}'
```

```json
{
  "success": true,
  "data_source": "real",
  "root_cause": "payment-worker",
  "causes": {
    "payment-worker": 0.847,
    "orders-api": 0.312
  },
  "backdoor_effects": {
    "payment-worker": 0.731
  },
  "actions_recommended": 3,
  "patterns_detected": 2,
  "confidence_score": 0.812,
  "confidence_state": "CONFIRMED",
  "latent_risk": "LOW",
  "fallback_triggered": false,
  "execution_time_ms": 47.3
}
```

---

### `POST /explain`

Returns the causal explanation — the evidence chain, affected nodes, and uncertainty per node.

```bash
curl -X POST http://localhost:8080/explain \
  -H "Content-Type: application/json" \
  -d '{"node_id":"orders-api","arrival_rate":340,"service_rate":280,"queue_length":42}'
```

```json
{
  "success": true,
  "data_source": "real",
  "root_cause": "payment-worker",
  "causes": ["payment-worker", "orders-api"],
  "effects": {
    "payment-worker": 0.731,
    "orders-api": 0.312
  },
  "uncertainty": {
    "payment-worker": 0.091,
    "orders-api": 0.184
  },
  "confidence_score": 0.812,
  "confidence_state": "CONFIRMED",
  "latent_risk": "LOW",
  "fallback_triggered": false,
  "execution_time_ms": 39.1
}
```

---

### `POST /act`

Returns ranked remediation recommendations. Requires authentication if `ABSIA_API_KEY` is set. Returns HTTP `503` with no actions when the safety gate fires — never a guess.

```bash
curl -X POST http://localhost:8080/act \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ABSIA_API_KEY" \
  -d '{"node_id":"orders-api","arrival_rate":340,"service_rate":280,"queue_length":42}'
```

```json
{
  "success": true,
  "data_source": "real",
  "recommended_actions": [
    {
      "action": "scale_out",
      "target": "payment-worker",
      "priority": 1,
      "confidence": 0.812,
      "expected_improvement": 0.38
    }
  ],
  "policy_trained": true,
  "confidence_score": 0.812,
  "confidence_state": "CONFIRMED",
  "latent_risk": "LOW",
  "fallback_triggered": false,
  "execution_time_ms": 52.6
}
```

When confidence is too low:

```json
{
  "success": false,
  "fallback_triggered": true,
  "confidence_state": "UNKNOWN",
  "latent_risk": "HIGH",
  "errors": ["safety gate: insufficient confidence to recommend actions"]
}
```

---

### `GET /metrics`

Prometheus-compatible metrics endpoint. Scrape with any Prometheus-compatible system or leave it alone — it's there if you need it.

```
# HELP absia_nodes_total Number of distinct nodes in the metrics store
# TYPE absia_nodes_total gauge
absia_nodes_total 4

# HELP absia_nodes_with_data Nodes with enough samples to run the pipeline (>=4)
# TYPE absia_nodes_with_data gauge
absia_nodes_with_data 4

# HELP absia_store_samples_total Total samples stored across all nodes
# TYPE absia_store_samples_total counter
absia_store_samples_total 192

# HELP absia_docker_available 1 if the Docker socket is reachable
# TYPE absia_docker_available gauge
absia_docker_available 1
```

---

## Configuration

Everything has a safe default. You only set what you need to change.

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP listen port inside the container |
| `ABSIA_API_KEY` | *(empty)* | Bearer token protecting `/act`. Leave empty to disable auth |
| `ABSIA_POLICY_STORE_PATH` | `/data/policies` | Where RL policy weights are persisted between restarts |
| `ABSIA_LOG_LEVEL` | `info` | `debug` · `info` · `warn` · `error` |
| `ABSIA_SEED` | `42` | Deterministic seed for reproducible pipeline runs |
| `ABSIA_RATE_LIMIT_RPS` | `10` | Requests per second per IP across analysis endpoints |
| `ABSIA_RATE_LIMIT_BURST` | `20` | Burst allowance above the sustained rate |
| `ABSIA_MAX_BODY_BYTES` | `1048576` | Maximum JSON request body size (1 MiB) |
| `ABSIA_READ_TIMEOUT_SECONDS` | `5` | HTTP server read timeout |
| `ABSIA_WRITE_TIMEOUT_SECONDS` | `30` | HTTP server write timeout (pipeline can take ~50ms) |
| `ABSIA_IDLE_TIMEOUT_SECONDS` | `120` | HTTP keep-alive idle timeout |

---

## Persisting Policy Weights

ABSIA's reinforcement learning policy learns which interventions work for your specific system. By default, that knowledge is lost on container restart. Mount a volume to keep it:

```yaml
services:
  absia:
    image: yourdockerhub/absia:latest
    ports:
      - "8080:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - absia_policies:/data/policies

volumes:
  absia_policies:
```

---

## Security

### Protecting `/act`

The `/act` endpoint recommends or triggers remediations. If anything in your stack automates on its output, lock it down:

```yaml
environment:
  ABSIA_API_KEY: "your-secret-key"
```

Then callers must include:

```
Authorization: Bearer your-secret-key
```

`/health`, `/nodes`, `/ingest`, `/analyze`, and `/explain` are unauthenticated by default — appropriate for internal networks.

### Docker Socket Access

Mounting `/var/run/docker.sock` gives ABSIA read access to your container metadata and stats. It uses this only to list running containers and poll CPU/memory. If your threat model requires isolation, you can skip the socket mount entirely — ABSIA falls back gracefully and waits for metrics pushed via `/ingest`.

### Network Boundaries

ABSIA makes no outbound network calls. It only listens on its configured port and reads from the Docker socket you mount. There are no telemetry calls, no license checks, no external dependencies at runtime.

---

## Building from Source

ABSIA has **zero external Go dependencies**. The only requirement is Go 1.23+.

```bash
git clone https://github.com/you/absia
cd absia
go build ./...
go test ./...
go run .
```

Build the Docker image:

```bash
docker build -t absia:latest .
```

The resulting image is **~8 MB** (scratch base + static binary + CA certificates).

---

## Architecture

```
Docker socket
     │
     ▼
pkg/docker          ← stdlib HTTP client over /var/run/docker.sock
     │
     ▼
pkg/autodetect      ← container discovery + stats → M/M/1 metrics (every 15s)
     │
     ▼
pkg/metricsstore    ← sliding-window time-series store (last 60 samples per node)
     │
     ▼
pkg/orchestrator    ← five-phase pipeline coordinator
     │
     ├── Phase 1: pkg/intelligence/phase1_signal    (signal physics, EMA processors)
     ├── Phase 2: pkg/intelligence/phase2_pattern   (change-point, divergence, drift)
     ├── Phase 3: pkg/intelligence/phase3_causal    (causal graph, do-calculus, d-sep)
     ├── Phase 4: pkg/intelligence/phase4_explanation (root-cause narrative, uncertainty)
     └── Phase 5: pkg/intelligence/phase5_insight   (RL policy, safety gate)
          │
          ▼
     pkg/api         ← HTTP handlers + embedded dashboard UI
```

The pipeline is fully deterministic given the same seed and input data. Tests reproduce exactly.

---

## FAQ

**Does ABSIA require Prometheus?**
No. It reads directly from the Docker socket. Prometheus is not required, not started, and not contacted unless you add it yourself.

**Does ABSIA modify my containers?**
No. It is read-only. It reads container metadata and stats. It never restarts, stops, or reconfigures anything.

**What if I don't mount the Docker socket?**
ABSIA starts normally and logs a warning. You can push metrics manually via `POST /ingest` instead. Everything else works.

**How many containers can it handle?**
The pipeline runs a causal graph over every node. Practically tested up to ~50 nodes. Beyond that, analysis latency increases but the system stays stable — the safety gate will flag low-confidence results automatically.

**Can I run multiple ABSIA instances?**
Yes, but they won't share state. Each instance maintains its own metrics store and policy weights. For a shared view, push from one instance to another via `/ingest`.

**The safety gate keeps returning UNKNOWN. Why?**
Usually one of: not enough data yet (need ≥4 samples per node), very high latent risk detected (hidden variable suspected in the causal graph), or ranking instability between consecutive runs (causal structure is changing faster than the window can capture). Check `/health` to confirm `real_data_available: true` and wait for more samples to accumulate.

---

## License

MIT — see [LICENSE](LICENSE).