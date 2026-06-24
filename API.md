# ABSIA API Reference

The ABSIA Engine exposes a lightweight HTTP API for integration, metrics ingestion, and inference triggering.

Base URL: `http://localhost:8080` (Default)

---

## `GET /health`

Returns the global health state of the ABSIA system. Used by load balancers and the UI.

**Response (200 OK):**
```json
{
  "status": "ok",
  "version": "2.1.0",
  "uptime_seconds": 3600
}
```

---

## `GET /nodes`

Lists all microservices (nodes) currently being tracked by the ABSIA engine, along with their current load metrics.

**Response (200 OK):**
```json
{
  "nodes": [
    {
      "id": "test-service",
      "load": 1.25,
      "pipeline_ready": true,
      "last_updated": "2026-06-24T10:00:00Z"
    }
  ]
}
```

---

## `POST /ingest`

Manually ingests queuing metrics for a specific node. Required if Docker auto-discovery is disabled.

**Request Body:**
```json
{
  "node_id": "payment-gateway",
  "arrival_rate": 150.5,
  "service_rate": 120.0,
  "queue_length": 45
}
```

**Response (200 OK):**
```json
{
  "success": true,
  "node_id": "payment-gateway",
  "status": "stored",
  "sample_count": 1,
  "load": 1.254,
  "pipeline_ready": false
}
```
*Note: `pipeline_ready` becomes `true` after 5 samples are collected.*

---

## `POST /analyze`

Triggers the full 5-phase Causal Inference pipeline for a specific node to determine root causes.

**Request Body:**
```json
{
  "node_id": "payment-gateway"
}
```

**Response (200 OK):**
```json
{
  "request_id": "abc123xyz",
  "execution_time_ms": 52.4,
  "confidence_score": 0.85,
  "incident_title": "payment-gateway needs help",
  "summary": "Severe queuing bottleneck detected.",
  "confidence_narrative": "I am 85% sure. Automated remediation is recommended.",
  "narrative": [
    "What is happening: payment-gateway is the service most likely starting the current problem.",
    "Why it matters: the service is receiving more work or holding more backlog than it can comfortably process.",
    "Impact: Unchecked, this will cause cascading latency across dependent services."
  ],
  "causes": [
    {
      "node": "payment-gateway",
      "probability": 0.85,
      "impact": "CRITICAL"
    }
  ],
  "remediation": [
    {
      "action": "Throttle incoming traffic to payment-gateway",
      "risk": "LOW"
    },
    {
      "action": "Scale up payment-gateway replicas",
      "risk": "MODERATE"
    }
  ]
}
```

**Error Responses:**
- `400 Bad Request`: If `node_id` is missing.
- `422 Unprocessable Entity`: If `pipeline_ready` is false (not enough historical data collected yet).
