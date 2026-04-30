# ABSIA API Documentation

Complete reference for all HTTP endpoints. All requests and responses use `Content-Type: application/json`.

The base URL in all examples is `http://localhost:8080`.

---

## Authentication

Only the `/act` endpoint requires authentication. When `ABSIA_API_KEY` is set, include:

```
Authorization: Bearer <your-api-key>
```

All other endpoints are unauthenticated. If `ABSIA_API_KEY` is not set, `/act` accepts requests without a token (development mode — log warning is emitted on startup).

---

## Rate Limiting

All endpoints except `/health` are rate-limited per client IP. The default is 10 requests/second with a burst of 20. When the limit is exceeded, the server returns HTTP 429.

Configure with `ABSIA_RATE_LIMIT_RPS` and `ABSIA_RATE_LIMIT_BURST`.

---

## Common Request Schema

`/ingest`, `/analyze`, `/explain`, and `/act` all accept the same request body:

```json
{
  "node_id": "string (optional, default: 'primary')",
  "arrival_rate": "float64 (required, >= 0)",
  "service_rate": "float64 (required, > 0)",
  "queue_length": "integer (required, >= 0)"
}
```

### Field Semantics

**`node_id`**  
Identifies the node (service, microservice instance, queue worker) being observed. Used as the target for causal analysis and for keying stored samples. If omitted, defaults to `"primary"`.

**`arrival_rate`** (λ)  
The rate at which work arrives at the node — requests per second, tasks per second, messages per second, etc. Must be ≥ 0.

**`service_rate`** (μ)  
The processing capacity of the node — the maximum rate at which it can complete work under full utilisation. Must be > 0.

**`queue_length`** (Q)  
The current number of items waiting in the queue (not yet being processed). Must be ≥ 0.

### Derived Metric: Utilisation ρ

ABSIA internally computes `ρ = λ / μ`:
- `ρ < 1.0` — stable, queue will drain
- `ρ = 1.0` — critically loaded
- `ρ > 1.0` — overloaded, queue grows without bound

---

## Endpoints

---

### `GET /health`

**Purpose:** Liveness and readiness probe. Use for Kubernetes `livenessProbe` and `readinessProbe`. Never rate-limited.

**Authentication:** None

**Request:** No body.

**Response 200:**
```json
{
  "status": "ok",
  "version": "2.1.0",
  "ready": true,
  "real_data_available": true,
  "ingested_node_count": 4,
  "auth_mode": "enabled"
}
```

**Fields:**

| Field | Type | Description |
|---|---|---|
| `status` | string | `"ok"` when server is running normally |
| `version` | string | ABSIA version string |
| `ready` | bool | `true` when pipeline can accept and process requests |
| `real_data_available` | bool | `true` when ≥1 node has ≥4 stored samples, or Prometheus is active |
| `ingested_node_count` | int | Number of distinct nodes with stored samples |
| `auth_mode` | string | `"enabled"` or `"disabled"` |

**curl:**
```bash
curl http://localhost:8080/health
```

---

### `POST /ingest`

**Purpose:** Store a metrics sample for a node. Samples accumulate in a per-node sliding window (default capacity: 60). Once a node has ≥4 samples, the pipeline uses real data instead of synthetic for that node.

**Authentication:** None  
**Rate limited:** Yes

**Request:**
```json
{
  "node_id": "api-gateway",
  "arrival_rate": 120.5,
  "service_rate": 95.0,
  "queue_length": 18
}
```

**Response 202:**
```json
{
  "success": true,
  "sample_count": 12,
  "node_id": "api-gateway",
  "pipeline_ready": true
}
```

**Fields:**

| Field | Type | Description |
|---|---|---|
| `success` | bool | `true` on successful ingest |
| `sample_count` | int | Total samples stored for this node after this ingest |
| `node_id` | string | The node ID that was stored |
| `pipeline_ready` | bool | `true` when this node now has ≥4 samples |

**curl:**
```bash
curl -s -X POST http://localhost:8080/ingest \
  -H 'Content-Type: application/json' \
  -d '{"node_id":"api-gateway","arrival_rate":120.5,"service_rate":95.0,"queue_length":18}'
```

**Batch ingest example (bash):**
```bash
for i in $(seq 1 10); do
  curl -s -X POST http://localhost:8080/ingest \
    -H 'Content-Type: application/json' \
    -d "{\"node_id\":\"db\",\"arrival_rate\":$((50+RANDOM%20)),\"service_rate\":40,\"queue_length\":$((RANDOM%15))}" &
done
wait
```

---

### `POST /analyze`

**Purpose:** Run all five pipeline phases. Returns the full causal analysis — root cause identification, effect magnitudes, backdoor-adjusted effects, and the safety gate result.

**Authentication:** None  
**Rate limited:** Yes

**Request:** Common schema (see above).

**Response 200:**
```json
{
  "success": true,
  "root_cause": "database",
  "physics_root_cause": "database",
  "causes": {
    "database": 0.812,
    "cache": 0.441,
    "api-gateway": 0.234
  },
  "backdoor_effects": {
    "database": 0.791,
    "cache": 0.388,
    "api-gateway": 0.198
  },
  "patterns_detected": 3,
  "actions_recommended": 2,
  "data_source": "real",
  "execution_time_ms": 142.7,
  "safety": {
    "confidence_state": "CONFIRMED",
    "confidence_score": 0.831,
    "latent_risk": "LOW",
    "graph_coverage": 0.91,
    "determinism": 0.87,
    "fallback_triggered": false,
    "fallback_reasons": [],
    "probe_recommendations": []
  }
}
```

**Fields:**

| Field | Type | Description |
|---|---|---|
| `root_cause` | string | Node identified as the causal origin. Empty string if no cause identified. |
| `physics_root_cause` | string | Node with highest utilisation ρ — the M/M/1 physics answer |
| `causes` | object | Map of node ID → causal effect score (0–1). Sorted descending. |
| `backdoor_effects` | object | Map of node ID → backdoor-adjusted ACE. More reliable than raw `causes`. |
| `patterns_detected` | int | Number of behavioural patterns detected in Phase 2 |
| `actions_recommended` | int | Number of intervention actions in the policy output |
| `data_source` | string | `"real"` or `"synthetic"` |
| `execution_time_ms` | float | Total wall-clock time for all five phases |
| `safety` | object | See Safety Gate schema below |

**Safety Gate schema:**

| Field | Type | Description |
|---|---|---|
| `confidence_state` | string | `CONFIRMED`, `PROBABLE`, or `UNKNOWN` |
| `confidence_score` | float | Raw score 0–1. ≥0.75 → CONFIRMED, ≥0.45 → PROBABLE |
| `latent_risk` | string | `LOW`, `MEDIUM`, or `HIGH`. HIGH → unconditional UNKNOWN |
| `graph_coverage` | float | Fraction of node-pairs with identified causal direction (0–1) |
| `determinism` | float | Consistency of causal attribution across signal perturbations (0–1) |
| `fallback_triggered` | bool | `true` when UNKNOWN was returned |
| `fallback_reasons` | array | Reasons for fallback (e.g. `"insufficient_samples"`, `"high_latent_risk"`) |
| `probe_recommendations` | array | Node IDs recommended for additional instrumentation |

**curl:**
```bash
curl -s -X POST http://localhost:8080/analyze \
  -H 'Content-Type: application/json' \
  -d '{"node_id":"primary","arrival_rate":12,"service_rate":8,"queue_length":5}' \
  | python3 -m json.tool
```

---

### `POST /explain`

**Purpose:** Run phases 1–4 only. Returns the causal explanation without the RL policy layer. Faster than `/analyze`. Use when you want causal structure without action recommendations.

**Authentication:** None  
**Rate limited:** Yes

**Request:** Common schema (see above).

**Response 200:**
```json
{
  "success": true,
  "root_cause": "database",
  "causes": ["database", "cache", "api-gateway"],
  "effects": {
    "database": 0.812,
    "cache": 0.441,
    "api-gateway": 0.234
  },
  "uncertainty": {
    "database": 0.08,
    "cache": 0.14
  },
  "data_source": "synthetic",
  "execution_time_ms": 88.3,
  "safety": { "...": "..." }
}
```

**Additional fields vs `/analyze`:**

| Field | Type | Description |
|---|---|---|
| `causes` | array | Ordered list of cause node IDs (most causal first) |
| `effects` | object | Effect magnitudes per node |
| `uncertainty` | object | Uncertainty estimates per node (lower is more certain) |

**curl:**
```bash
curl -s -X POST http://localhost:8080/explain \
  -H 'Content-Type: application/json' \
  -d '{"arrival_rate":15,"service_rate":10,"queue_length":8}'
```

---

### `POST /act`

**Purpose:** Run the full pipeline including the RL policy layer. Returns ranked intervention recommendations. The safety gate strictly blocks actions when confidence is insufficient.

**Authentication:** Bearer token required when `ABSIA_API_KEY` is set.  
**Rate limited:** Yes

**Request:** Common schema (see above).

**Response 200 (safety gate CONFIRMED or PROBABLE):**
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
        "cache": 0.38
      }
    }
  ],
  "policy_trained": true,
  "data_source": "real",
  "execution_time_ms": 187.4,
  "safety": {
    "confidence_state": "CONFIRMED",
    "confidence_score": 0.831,
    "latent_risk": "LOW",
    "...": "..."
  }
}
```

**Action fields:**

| Field | Type | Description |
|---|---|---|
| `policy_rank` | int | Rank by policy score (1 = best) |
| `policy_score` | float | Normalised policy score 0–1 |
| `interventions` | object | Map of node ID → recommended target utilisation (do(X = v)) |

**Response 503 (safety gate UNKNOWN):**
```json
{
  "success": false,
  "safety_blocked": true,
  "confidence_state": "UNKNOWN",
  "confidence_score": 0.31,
  "latent_risk": "HIGH",
  "probe_recommendations": ["cache", "worker-pool"],
  "fallback_reasons": ["high_latent_risk", "insufficient_graph_coverage"],
  "message": "Safety gate UNKNOWN: automated action blocked. Instrument probe nodes and retry."
}
```

HTTP 503 is intentional. It signals a retriable safety condition — not a server error. Orchestrators should back off and retry after adding instrumentation to the `probe_recommendations` nodes.

**Response 401 (bad or missing token):**
```json
{
  "success": false,
  "errors": ["invalid or missing Authorization header"]
}
```

**curl (no auth):**
```bash
curl -s -X POST http://localhost:8080/act \
  -H 'Content-Type: application/json' \
  -d '{"arrival_rate":12,"service_rate":8,"queue_length":5}'
```

**curl (with auth):**
```bash
curl -s -X POST http://localhost:8080/act \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer my-secret-key' \
  -d '{"arrival_rate":12,"service_rate":8,"queue_length":5}'
```

---

## Error Reference

All errors use this envelope:

```json
{
  "success": false,
  "errors": ["human-readable error message"]
}
```

| HTTP Status | Trigger |
|---|---|
| `400 Bad Request` | Malformed JSON body |
| `401 Unauthorized` | Missing/invalid `Authorization` on `/act` |
| `405 Method Not Allowed` | Wrong HTTP method (e.g. GET on `/analyze`) |
| `413 Request Entity Too Large` | Body exceeds `ABSIA_MAX_BODY_BYTES` |
| `422 Unprocessable Entity` | Validation failed — field errors in `errors` array |
| `429 Too Many Requests` | Per-IP rate limit exceeded |
| `503 Service Unavailable` | Safety gate `UNKNOWN` on `/act` — retriable |

---

## Prometheus Integration

If `PROMETHEUS_URL` is set, ABSIA polls these metrics every 15 seconds and maps them to (λ, μ, Q) triples:

| Prometheus Metric | Mapped To |
|---|---|
| `container_cpu_usage_seconds_total` | arrival_rate proxy |
| `http_requests_total` | arrival_rate (primary) |
| `http_request_duration_seconds` | service_rate (via throughput) |
| `container_memory_usage_bytes` | queue_length proxy |

Polled data is stored in the same `metricsstore` as `/ingest` data. The `real_data_available` field on `/health` goes `true` once any node has ≥4 samples from either source.

---

## Complete Workflow Example

```bash
# 1. Check system health
curl http://localhost:8080/health

# 2. Ingest 6 samples for two nodes (above the 4-sample threshold)
for i in 1 2 3 4 5 6; do
  curl -s -X POST http://localhost:8080/ingest \
    -H 'Content-Type: application/json' \
    -d '{"node_id":"api","arrival_rate":80,"service_rate":60,"queue_length":12}'
  curl -s -X POST http://localhost:8080/ingest \
    -H 'Content-Type: application/json' \
    -d '{"node_id":"database","arrival_rate":60,"service_rate":40,"queue_length":25}'
done

# 3. Run full analysis (will use real data now)
curl -s -X POST http://localhost:8080/analyze \
  -H 'Content-Type: application/json' \
  -d '{"node_id":"database","arrival_rate":60,"service_rate":40,"queue_length":25}' \
  | python3 -m json.tool

# 4. Get causal explanation
curl -s -X POST http://localhost:8080/explain \
  -H 'Content-Type: application/json' \
  -d '{"node_id":"database","arrival_rate":60,"service_rate":40,"queue_length":25}'

# 5. Request intervention actions
curl -s -X POST http://localhost:8080/act \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer my-key' \
  -d '{"node_id":"database","arrival_rate":60,"service_rate":40,"queue_length":25}'
```
