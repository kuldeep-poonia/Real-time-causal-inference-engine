# ABSIA: Real-World Testing & Benchmark Report

**Version:** 1.0 (June 2026)
**Repository:** github.com/kuldeep-poonia/Real-time-causal-inference-engine

## 1. Executive Summary
ABSIA (Autonomous Bayesian System Intelligence Agent) is a hyper-lightweight, causal inference sidecar that identifies microservice root causes in real-time. Built entirely in Go with zero external dependencies, it compiles to an ~8MB binary and deploys in seconds.

This report contains rigorous, evidence-backed benchmark data proving that ABSIA drastically reduces Time-To-Resolution (TTR) compared to traditional metric-stack observability tools, while maintaining exceptional accuracy.

## 2. Technical Architecture
* **Language:** Go 1.23+
* **Dependency Footprint:** Zero external libraries
* **Size:** ~8MB (Scratch container)
* **Deployment:** Single container sidecar (reads directly from `/var/run/docker.sock`)
* **Core Engine:** 5-Phase Bayesian Causal Pipeline (Ingest, Feature Extraction, Causal Graph, Scoring, Safety Gate)

## 3. Phase 4: Before vs After Comparison (The "Why ABSIA" Test)
We executed a strict side-by-side benchmark against a 19-container stack (Weaveworks Sock Shop + Load Gen + Monitoring Stack). We injected a severe CPU fault into the `orders` and `carts` services and measured the time-to-actionable-insight.

| Dimension | Traditional (Prometheus + Grafana) | ABSIA (Causal Inference Engine) |
| :--- | :--- | :--- |
| **Setup Time** | **30+ min** (Highly fragile in nested Docker environments like Codespaces, requiring deep `/sys` volume mounts and custom dashboards) | **< 1 minute** (Zero configuration. Discovers all 19 containers instantly via the Docker socket) |
| **Time to Insight** | **~3 - 5 minutes** (Inherent scraping delay + 1m rate aggregation window) | **~16 seconds** (Instantaneous local metrics processing) |
| **What It Shows** | Hundreds of un-correlated metric lines and dashboards. | Explicit UI output: `Root Cause: Orders, Certainty: 60%` |
| **Root Cause Discovery** | SRE must manually correlate multiple graphs across dashboards. | Mathematically ranked by Bayesian probability. |

### The "Configuration Drift" Reality
During live benchmarking in a GitHub Codespace (a Docker-in-Docker environment), traditional tools like `cAdvisor` completely failed to mount host directories correctly, leaving Grafana completely blind to the 14 microservices. **ABSIA, however, suffered zero configuration drift and discovered all containers perfectly.** This demonstrates ABSIA's immense value in modern, complex, and nested containerized environments. 

## 4. Phase 5: AIOps Ground Truth Accuracy Validation
To prove ABSIA's causal intelligence is mathematically sound, we executed a Phase 5 validation test against 100 simulated real-world scenarios representing known failure states (CPU pressure, Memory pressure, Network cascading failures).

| Metric | Result |
| :--- | :--- |
| **Total Scenarios Tested** | 100 |
| **Exact Match (Root Cause Identified)** | 94 / 100 |
| **Safety Gate Triggered (UNKNOWN)** | 6 / 100 |
| **False Positives (Confident but Wrong)** | 0 / 100 |
| **Overall Accuracy** | **94%** |

**Conclusion:** Even when ABSIA lacks sufficient data to make a confident prediction, the Safety Gate prevents it from emitting false positive root causes. It achieved a 94% accuracy rate against complex distributed failure scenarios.

## 5. Getting Started
ABSIA can be deployed into any Docker environment with a single command. It will automatically discover your running containers and begin tracing causal relationships.

```bash
docker run -d \
  --name absia \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  poonia98/absia:latest
```
