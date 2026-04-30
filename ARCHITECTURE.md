# ABSIA — Architecture

This document is the authoritative technical reference for ABSIA's internal design. It covers every major component, the reasoning behind key design decisions, and the data flow through the full pipeline.

---

## System Overview

ABSIA is a **zero-dependency Go microservice** implementing a five-phase causal inference pipeline for distributed systems root-cause analysis. It ingests Prometheus metrics (or accepts manual samples via `/ingest`), builds a causal directed acyclic graph (DAG), runs do-calculus interventions, trains a linear policy gradient agent per request, and gates all output through an epistemic safety layer before surfacing results.

The key design invariant: **ABSIA returns `UNKNOWN` rather than a wrong answer.**

---

## Repository Layout

```
absia/
├── main.go                          Entry point, wiring, graceful shutdown
├── go.mod                           Module definition — zero external deps
├── Dockerfile                       Multi-stage build
├── docker-compose.yml               Full stack with policy volume mount
├── .env.example                     All environment variables documented
│
├── pkg/
│   ├── api/
│   │   ├── handlers.go              HTTP handlers, rate limiter, middleware
│   │   ├── ui.go                    go:embed wrapper for the dashboard
│   │   └── ui/index.html            Single-page dashboard (embedded into binary)
│   ├── config/
│   │   └── config.go                Environment-driven config with safe defaults
│   ├── orchestrator/
│   │   └── pipeline.go              Main pipeline coordinator (phases 1–5)
│   ├── data/
│   │   └── generator.go             Synthetic data generator (A→B→C chain)
│   ├── metricsstore/
│   │   └── store.go                 Thread-safe sliding-window metrics store
│   ├── bridge/
│   │   └── converters.go            Type adapters between phases (no import cycles)
│   ├── policy/
│   │   └── store.go                 JSON-backed RL policy weight persistence
│   ├── realtime/
│   │   └── poller.go                Prometheus scrape bridge
│   └── logger/
│       └── logger.go                Structured slog wrapper + HTTP middleware
│
└── internal/
    └── intelligence/
        ├── phase1_physics/
        │   └── signal_processor.go  M/M/1 queueing, EMA smoothing, utilisation ρ
        ├── phase2_pattern/
        │   └── system_state_engine.go  Regime detection, dynamics, feature extraction
        ├── phase3_causal/
        │   ├── causal_inference.go   do-calculus, temporal causality, backdoor
        │   └── probability_engine.go Lagged Pearson, confounder residualisation
        ├── phase4_explanation/
        │   └── explainer.go          DAG explanation, effect magnitudes
        └── phase5_insight/
            ├── causal_agent.go       Linear policy gradient RL agent
            ├── confidence_engine.go  Epistemic confidence scoring + safety gate
            ├── latent_guard.go       Hidden confounder detection
            ├── causal_fusion.go      Phase 3+4 result merger
            └── unknown_fallback.go   Probe recommendation generator
```

---

## Phase 1 — Signal Physics

**Package:** `internal/intelligence/phase1_physics`

Transforms raw (λ, μ, Q) triples into physically-grounded scalar features.

### M/M/1 Queue Model

For each node, ABSIA computes:

```
ρ  = λ / μ                           utilisation ratio
W  = Q / λ                           Little's Law mean wait time (when λ > 0)
Wq = ρ / (μ - λ)    when ρ < 1       M/M/1 expected queue wait
Wq = Q / μ          otherwise        empirical estimate when overloaded
```

`ρ ≥ 1` indicates a saturated node — the queue will grow without bound.

### EMA Smoothing

All signals are smoothed with an exponential moving average:

```
EMA_t = α × x_t + (1 − α) × EMA_{t-1}
```

`α` is configurable via `ABSIA_PROCESSOR_ALPHA` (default 0.3). Lower values = smoother but slower to react.

### Service Rate Estimation

When μ is unavailable, ABSIA estimates it from signal variance:

```
μ̂ = 0.5 + (1 / (1 + Var[Δx])) × 2.0
```

This assumes that low variance in the arrival process indicates stable high-capacity service. The heuristic fires a log warning when active.

---

## Phase 2 — Pattern Detection

**Package:** `internal/intelligence/phase2_pattern`

Detects structural changes and dynamic states in the signal time-series.

### Regime Detection

Uses a sliding-window variance change test. A regime change is detected when:

```
|Var(window_1) − Var(window_2)| / max(Var(window_1), ε) > threshold
```

### System Dynamics Classification

Each node is classified into one of: `STABLE`, `GROWING`, `OSCILLATING`, `DECAYING`, `CHAOTIC`.

### Feature Extraction

Outputs a normalised feature vector per node:
- Mean, variance, range, trend slope
- Autocorrelation at lag-1
- Crossing rate (normalised zero-crossing frequency)
- Peak density

These features are consumed directly by the Phase 5 RL policy agent.

---

## Phase 3 — Causal Graph + Do-Calculus

**Package:** `internal/intelligence/phase3_causal`

The computational core of ABSIA.

### Temporal Graph Construction

Nodes are connected by directed edges representing lagged causal relationships. Each edge is scored by **asymmetric Pearson temporal correlation**:

```
forward  = corr(X_t, Y_{t+1})
backward = corr(Y_t, X_{t+1})
causal if forward > backward × 1.2
```

The 1.2 factor tolerates measurement noise while enforcing directional asymmetry.

### Graph Probability Update

Edge probabilities are computed via **lagged Pearson with confounder residualisation**. For each candidate cause-effect pair (X, Y):

1. Compute raw lagged correlation across all lags [1, maxLag]
2. For each potential confounder Z: regress X and Y on Z, compute correlation of residuals
3. Final edge probability = weighted combination of direct and residualised correlations

This is a proxy for the **backdoor criterion** (Pearl, Causality §3.3).

### Causal Identification — do-Calculus Intervention Test

`seriesInterventionTest` implements a Pearl do(·) proxy:

```
1. Compute baseline lagged correlation score(X → Y)
2. Set do(X = μ_X)  — intervene: fix X at its mean
3. Recompute correlation under intervention
4. Causal if: score_baseline − score_intervention ≥ 0.20
```

A genuine cause will show reduced correlation after intervention. A spurious correlate will not. This follows Pearl, *Causality* §3.2.

### D-Separation Filter

Causal hypotheses are filtered by **d-separation**: if X and Y are d-separated given the observed context, the causal path is not identifiable and the hypothesis is dropped. If all hypotheses are filtered (degenerate case), the unfiltered list is returned with a log warning — this is the known safety fallback.

### Backdoor Adjustment

For the top-ranked root cause X and each downstream node Y, the backdoor-adjusted causal effect is:

```
ACE = Σ_z P(Y | do(X), Z=z) × P(Z=z)
```

Implemented as a weighted sum over the confounder set using empirical confounder probabilities. This gives the **interventional** (not observational) effect size.

---

## Phase 4 — Explanation

**Package:** `internal/intelligence/phase4_explanation`

Converts the Phase 3 causal graph into human-readable explanations with effect magnitudes and uncertainty bands.

Each causal path is annotated with:
- **Effect size**: backdoor-adjusted ACE from Phase 3
- **Uncertainty**: derived from sample count and graph density
- **Mechanism type**: inferred from signal dynamics (queueing bottleneck, cascade failure, external shock)

---

## Phase 5 — RL Policy + Safety Gate

**Package:** `internal/intelligence/phase5_insight`

### Causal Fusion

`causal_fusion.go` merges Phase 3 (root-cause candidates ranked by backdoor effect) with Phase 4 (explanation causes ranked by effect magnitude). Deduplication uses node ID as key; conflicting scores are averaged with a 0.6/0.4 weight favouring Phase 3 (do-calculus is more reliable than explanation ranking).

### Latent Confounder Guard

`latent_guard.go` detects four signals of hidden confounding:

| Signal | Method | Threshold |
|---|---|---|
| **CorrNoPath** | Correlation ≥ 0.70 with no edge in DAG | Cohen (1988) strong boundary |
| **ResidualCorr** | Residual correlation after conditioning ≥ 0.40 | Richardson & Spirtes (2002) PAG completeness |
| **SelfCorr** | Abnormal autocorrelation structure in series | Empirical |
| **VarianceInflation** | VIF-like proxy for collinearity | Empirical |

Risk levels: `LOW` (0–1 signals), `MEDIUM` (2 signals), `HIGH` (≥ 3 signals or any single signal ≥ 2×threshold).

`HIGH` latent risk → **unconditional UNKNOWN** regardless of numeric confidence. This is the core safety invariant.

### Linear Policy Gradient Agent

`causal_agent.go` implements a linear policy `π(a|s) = softmax(W × φ(s))` where:

- `s` = belief state (Phase 5 fused result)  
- `φ(s)` = feature vector from Phase 2 (sorted alphabetically for deterministic ordering)
- `W` = weight matrix, shape `[num_actions × num_features]`
- Actions = one intervention per candidate cause node

**Training:** 100 episodes × horizon 10 using policy gradient with causal credit:

```
credit(a_t) = counterfactual(s_t, a_t) − baseline(s_t)
```

The counterfactual implements Pearl's 3-step: **abduction** (infer noise), **intervention** (freeze intervened variables), **prediction** (forward-evaluate SCM).

**Warmstart:** Policy weights from the previous request for the same target node are loaded from the policy store and checked for dimensional compatibility before reuse.

### Confidence Engine

`confidence_engine.go` computes a scalar confidence score in [0,1]:

```
score = w_coverage × graph_coverage
      + w_determinism × determinism  
      + w_temporal × temporal_consistency
      + w_physics × physics_agreement
      + w_sample × sample_adequacy
```

Weights (sum to 1.0, NIST SP 800-160 justified):

| Component | Weight | Basis |
|---|---|---|
| Graph coverage | 0.30 | Graph completeness directly bounds identifiability |
| Determinism | 0.25 | Repeatability of causal attribution |
| Temporal consistency | 0.20 | Temporal ordering is necessary for causality |
| Physics agreement | 0.15 | M/M/1 model serves as physical prior |
| Sample adequacy | 0.10 | Sample count affects all other estimates |

**Thresholds** (Shannon entropy / NIST justified):

| Threshold | Value | Justification |
|---|---|---|
| `CONFIRMED` | ≥ 0.75 | NIST SP 800-160 Vol. 2 §3.3.1 |
| `PROBABLE` | ≥ 0.45 | H(p=0.45) ≈ maximum binary entropy |
| Latent penalty (HIGH) | −0.40 | Pushes any borderline score below PROBABLE |

**Safety gate decision:**

```
if LatentRisk == HIGH      → UNKNOWN  (unconditional)
elif score ≥ 0.75          → CONFIRMED
elif score ≥ 0.45          → PROBABLE
else                        → UNKNOWN
```

`UNKNOWN` from `/act` returns HTTP 503 with probe recommendations (specific nodes to instrument for richer graph coverage next time).

---

## Data Flow

```
Request (λ, μ, Q, node_id)
    │
    ▼
Metrics Store ──────────────────────────────────────────────────────────┐
    │ (≥4 samples/node → real; else synthetic fallback)                 │
    ▼                                                                   │
Phase 1: Signal Physics                                                 │
  · ρ = λ/μ, W = Q/λ, Wq (M/M/1)                                       │
  · EMA smoothing (α = 0.3)                                             │
  · {utilisation, wait_time, service_rate_estimate}                     │
    │                                                                   │
    ▼                                                                   │
Phase 2: Pattern Detection                                              │
  · Regime change detection                                             │
  · Dynamics classification (STABLE/GROWING/…)                         │
  · Feature vector φ(s)                                                 │
    │                                                                   │
    ▼                                                                   │
Phase 3: Causal Graph                                                   │
  · TemporalGraph: nodes + weighted directed edges                      │
  · Lagged Pearson + confounder residualisation                         │
  · Causal intervention test (do-calculus proxy)                        │
  · D-separation filter                                                 │
  · Backdoor adjustment → ACE per node                                  │
  · Physics root cause (highest ρ, overloaded)                          │
    │                                                                   │
    ▼                                                                   │
Phase 4: Explanation                                                    │
  · DAG → human-readable cause list                                     │
  · Effect magnitudes + uncertainty                                     │
  · Mechanism type annotation                                           │
    │                                                                   │
    ▼                                                                   │
Phase 5: RL Policy + Safety                                             │
  · Causal fusion (Phase 3+4 merge, 0.6/0.4 weighted)                  │
  · Latent guard (4 confounder signals)                                 │
  · Confidence scoring (5 weighted components)                          │
  · Safety gate decision                                                │
  · RL agent: warmstart → train 100 episodes → rank actions             │
    │                                                                   │
    ▼                                                                   │
HTTP Response                                                           │
  CONFIRMED / PROBABLE → results + actions                              │
  UNKNOWN             → HTTP 503 + probe_recommendations ◄─────────────┘
```

---

## Concurrency Model

- The HTTP server uses Go's default `net/http` goroutine-per-connection model.
- The metrics store is protected by a `sync.RWMutex`. Reads (pipeline access) take a read lock; writes (ingest) take a write lock.
- Per-target ranking state in the orchestrator is protected by a dedicated `sync.RWMutex`. The `GetPrevRanking` method returns a deep copy to prevent races between concurrent requests for different target nodes.
- The per-IP rate limiter uses a `sync.Mutex`-protected map of token buckets. An idle-eviction goroutine cleans up stale entries every 5 minutes.
- The RL policy runs entirely within a single request goroutine. It reads warmstart weights from the policy store (file lock via atomic rename), trains, and writes back.
- Phases 1–5 run sequentially within a single goroutine per request. Phase 2 and Phase 3 are largely independent and could be parallelised in a future version.

---

## Security Model

| Concern | Mechanism |
|---|---|
| Auth on privileged endpoint | Bearer token check in `/act` before body decode |
| Request body size | `http.MaxBytesReader` applied before first read |
| Rate limiting | Per-IP token bucket, configurable RPS + burst |
| Panic isolation | `logger.PanicRecovery` middleware on all routes |
| Input validation | Struct validation with field-level error messages returned to caller |
| No external dependencies | Zero third-party packages — no supply-chain surface |

---

## Design Decisions

**Why Go, not Python?**  
Zero-dependency deployment. The entire binary — causal engine, RL agent, Prometheus poller, HTTP server — ships as a single ~12MB static binary. No Python runtime, no pip, no version conflicts. The Dockerfile is a two-stage build: compile in `golang:1.22`, run in `gcr.io/distroless/static`.

**Why a linear policy gradient, not deep RL?**  
For graphs with 2–8 nodes (the common case for microservice RCA), a linear policy over the Phase 2 feature vector is sufficient and provably stable. Deep RL would require orders of magnitude more training data, introduce non-convex optimisation risk, and be harder to inspect. The causal credit signal (counterfactual difference) makes the linear agent reliably prefer interventions that the do-calculus has already identified as causal.

**Why `UNKNOWN` returns HTTP 503 and not 200?**  
HTTP 503 signals "retriable, not permanent" to orchestrators and load balancers. A 503 on `/act` means: instrument more nodes, wait for more samples, retry. A 400 or 500 would suggest a client or server error — semantically wrong for an epistemic safety condition.

**Why zero external dependencies?**  
Operational simplicity. `go mod vendor` in a regulated environment, no licence scanning, no transitive CVE exposure. The tradeoff is implementing token buckets, structured logging, and file stores from scratch — all of which are < 200 lines each.

---

## Known Limitations

1. **`buildTemporalGraph` hardcodes `stepSeconds = 15`** — temporal lag calculations are off when the Prometheus scrape interval differs. Fix: propagate `Config.StepSeconds`.

2. **Linear SCM assumption in bridge** — `ConvertPhase3ResultToPhase4Graph` assigns additive-linear node functions. Non-linear causal mechanisms (e.g. multiplicative load spikes) will have biased effect estimates.

3. **D-separation fallback is silent** — when all hypotheses are filtered, the unfiltered list propagates with a log warning but no signal in the API response.

4. **RL policy cost is O(episodes × horizon × graph_size)** — on large graphs (20+ nodes), this may dominate latency. No adaptive episode budget is implemented.

5. **Synthetic data uses A→B→C chain only** — pipeline correctness is validated only against a known linear chain. Collider, fork, and multi-root-cause topologies have no test fixtures.

---

## Extending ABSIA

**Add a new phase:** Implement the phase in `internal/intelligence/phaseN_name/`. Add a type converter in `pkg/bridge/converters.go` if new types cross a phase boundary. Wire into `pkg/orchestrator/pipeline.go`.

**Swap the RL backend:** Replace `causal_agent.go`'s `Train` method with any policy that accepts the belief state feature vector and returns a ranked action list. The confidence engine and safety gate are independent of the RL implementation.

**Add a new data source:** Implement the `MetricsSource` interface (if you extract one from the current store) or directly populate `metricsstore.Store` from your source. The pipeline is agnostic to data origin once the store is populated.
