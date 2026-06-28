<div align="center">
  <h1>ABSIA: Real-Time Causal Inference Engine</h1>
  <p><b>Deterministic Root-Cause Analysis for Distributed Systems</b></p>
</div>

<br/>

## 🚀 The Core Product

**ABSIA** (Automated Bottleneck Surveillance & Intervention Architecture) is a mathematical, real-time causal inference engine built to diagnose distributed systems (like microservices and Docker containers) at the foundational physics level.

**This is not an AI project.** It does not use LLMs, Generative AI, or fuzzy machine learning. 

ABSIA is a highly deterministic engine rooted in **Queueing Theory** and **Structural Causal Models (SCM)**. It ingests traffic metrics, calculates the physical queuing limits of your system (arrival rates vs. service rates), builds a Directed Acyclic Graph (DAG) of your network, and mathematically proves exactly which microservice is causing a cascading failure across your infrastructure.

---

## ⚡ The Problem: Observability is Broken

Modern observability tools (Prometheus, Grafana, Datadog) are fundamentally reactive. They show you *symptoms*. 
When a system goes down, your dashboard turns red with hundreds of cascading 500 errors across 10 different services. Your engineers are forced to manually correlate logs, trace IDs, and metrics to guess where the failure originated.

### The ABSIA Solution

ABSIA shifts observability from **correlation** to **causation**. 

Instead of alerting you that "Service C is slow," ABSIA calculates the causal path to tell you: *"Service A's service rate has dropped to 10 req/sec, causing a queue buildup that is mathematically starving Service B and crashing Service C. Restart Service A."*

---

## 🏎️ Quick Start

ABSIA is compiled as a single, static, highly-optimized Go binary. It is incredibly lightweight and can be deployed via Docker in seconds.

```bash
# Deploy the ABSIA Engine
docker run -d \
  --name absia-engine \
  -p 8080:8080 \
  -v absia_policies:/data/policies \
  poonia98/absia:latest
```

Once deployed, ABSIA will automatically begin discovering local Docker containers (if configured) or wait for manual data ingestion at `POST /ingest`. 

You can view the real-time diagnostics dashboard by navigating to `http://localhost:8080`.

---

## 🧭 The Premium Diagnostics Dashboard

ABSIA features a pristine, Apple-style light mode dashboard that requires zero configuration. It is designed to be readable by anyone from a Principal Engineer to a Product Manager.

- **Live Service Discovery:** Tracks the active queuing load of all discovered services.
- **Incident Explanation:** Translates raw SCM mathematics into a plain-English narrative detailing exactly where the bottleneck is.
- **Recommended Actions:** Provides high-confidence, actionable remediation steps (e.g., Throttle, Scale, Restart) based on physical system constraints.

---

## 📖 Deep Dive Documentation

For engineers, architects, and engineering managers evaluating ABSIA, please read our exhaustive documentation suite:

- [**BENCHMARK.md**](./BENCHMARK.md) - The rigorous Phase 4 and Phase 5 empirical testing results, proving ABSIA's TTR reduction against Prometheus/Grafana.
- [**DOCUMENTATION.md**](./DOCUMENTATION.md) - The massive, exhaustive guide to ABSIA. Covers detailed mechanics, Return on Investment (ROI), failure semantics, and the exact mathematics used.
- [**ARCHITECTURE.md**](./ARCHITECTURE.md) - The engineering blueprint. Contains Mermaid.js dependency graphs and the 5-phase inference pipeline.
- [**API.md**](./API.md) - The complete HTTP API contract for custom integrations.

---
<div align="center">
  <i>Deterministic Infrastructure Intelligence.</i>
</div>