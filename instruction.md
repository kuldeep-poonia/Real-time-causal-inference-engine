# REAL-TIME CAUSAL INFERENCE ENGINE

## MASTER ANALYSIS & REFACTOR INSTRUCTION PROTOCOL

You are NOT allowed to act as a code generator.

You are NOT allowed to immediately start coding.

You are NOT allowed to perform repository-wide modifications.

You are NOT allowed to make assumptions.

You are NOT allowed to hallucinate architecture decisions.

You are NOT allowed to introduce theoretical improvements without proving they fit the existing system.

Your first responsibility is COMPLETE PROJECT UNDERSTANDING.

---

# PRIMARY OBJECTIVE

Perform a complete enterprise-grade forensic analysis of the entire repository.

The objective is to:

* Understand every folder
* Understand every file
* Understand every function
* Understand every dependency
* Understand every execution path
* Understand every data flow
* Understand every control flow
* Understand every mathematical model
* Understand every physics model
* Understand every theorem
* Understand every algorithm
* Understand every configuration
* Understand every test
* Understand every API contract

BEFORE making any modification.

No coding before understanding.

No refactoring before understanding.

No optimization before understanding.

No architectural changes before understanding.

---

# ANALYSIS SCOPE (MANDATORY)

This review is strictly focused on:

* software architecture
* system design
* reliability
* maintainability
* scalability
* observability
* correctness
* testing quality
* operational robustness
* mathematical validity
* physical model validity
* causal model validity
* production readiness

The purpose of this review is engineering excellence.

This review is NOT a security audit.

Do NOT perform:

* vulnerability discovery
* exploitability analysis
* penetration testing
* attack simulation
* exploit development
* red-team analysis
* offensive security research
* adversarial assessment

If security-related code exists, it may only be evaluated from:

* correctness
* maintainability
* reliability
* architectural consistency
* operational behavior

perspectives.

The goal is system quality and engineering rigor.


# ANALYSIS ORDER (MANDATORY)

Repository-wide analysis is FORBIDDEN.

Analysis must happen incrementally.

Folder-by-folder.

File-by-file.

Function-by-function.

Dependency-by-dependency.

Execution-path-by-execution-path.

---

# STAGE 0 – REPOSITORY MAPPING

First create a complete dependency graph.

Identify:

* folder hierarchy
* package hierarchy
* module hierarchy
* imports
* exports
* interfaces
* implementations
* entrypoints
* service boundaries
* execution paths
* runtime dependencies
* compile dependencies

Create:

* package dependency graph
* function call graph
* data flow graph
* control flow graph

No code modifications allowed.

---

# STAGE 1 – FOLDER ANALYSIS

Analyze folders one at a time.

Example workflow:

Folder A
├─ File 1
├─ File 2
├─ File 3

Complete Folder A first.

Only then move to Folder B.

Never jump across unrelated folders.

Never scan entire repository at once.

---

# STAGE 2 – FILE ANALYSIS

For every file:

Determine:

1. Why file exists
2. What responsibility it owns
3. What calls it
4. What it calls
5. Runtime importance
6. Architectural importance
7. Failure impact
8. Technical debt level
9. Refactor risk
10. Production risk

Document all findings.

No modifications yet.

---

# STAGE 3 – FUNCTION ANALYSIS

For every function:

Determine:

* purpose
* inputs
* outputs
* side effects
* state mutations
* dependencies
* callers
* callees
* complexity
* concurrency behavior
* memory behavior
* error handling
* edge cases

Classify:

* critical
* important
* auxiliary
* dead
* obsolete
* duplicated

Every function must be traced.

No exceptions.

---

# STAGE 4 – DEAD CODE INVESTIGATION

Search for:

* dead code
* unreachable code
* orphaned files
* unused interfaces
* unused structs
* unused methods
* unused functions
* unused constants
* unused variables
* unused packages
* duplicate logic
* abandoned implementations

Nothing may be deleted until proof is produced.

Every deletion requires evidence.

Evidence must include:

* call graph
* references
* dependency validation

---

# STAGE 5 – HARDCODED LOGIC INVESTIGATION

Search entire project for:

* hardcoded rules
* hardcoded thresholds
* hardcoded limits
* hardcoded constants
* hardcoded assumptions
* static coefficients
* fixed heuristics
* fixed weights
* fixed probabilities

Every occurrence must be documented.

Every occurrence must be justified.

If not justified:

mark for redesign.

---

# STAGE 6 – MAGIC NUMBER INVESTIGATION

Identify all:

* magic numbers
* unexplained coefficients
* unexplained multipliers
* unexplained thresholds
* unexplained constants

For every value:

Determine:

* origin
* purpose
* mathematical basis
* scientific basis
* engineering basis

If basis cannot be proven:

mark invalid.

---

# STAGE 7 – MATH & PHYSICS VALIDATION

For every formula:

Identify:

* theorem
* equation
* model
* derivation
* scientific source

Mandatory validation process:

1. Research first
2. Verify applicability
3. Verify assumptions
4. Verify constraints
5. Verify limitations

Never trust comments.

Never trust variable names.

Never trust documentation.

Validate actual implementation.

Every theorem must be proven relevant before usage.

No theorem may be added without research evidence.

No equation may be added without justification.

No formula may be added because it "sounds advanced".

---

# STAGE 8 – EVIDENCE-BASED RESEARCH REQUIREMENT

Before proposing any:

* theorem
* equation
* formula
* optimization method
* queueing model
* stochastic model
* forecasting model
* control theory method
* causal inference method
* physical model
* mathematical model

Perform evidence-based research.

Research must answer:

* Why is this model appropriate?
* What assumptions does it make?
* Under which conditions does it work?
* Under which conditions does it fail?
* What alternatives exist?
* Why is it superior to alternatives for this repository?

No assumptions allowed.

No hallucinations allowed.

No speculative improvements allowed.

Complexity alone is not justification.

Every recommendation must provide measurable value.

---

# STAGE 9 – ARCHITECTURE REVIEW

Evaluate:

* coupling
* cohesion
* modularity
* scalability
* observability
* maintainability
* extensibility
* reliability
* fault tolerance

Identify:

* bottlenecks
* anti-patterns
* architectural debt
* hidden dependencies
* fragile components

Document all findings.

---

# STAGE 10 – ENTERPRISE TESTING REVIEW

Basic unit-test mentality is forbidden.

Testing must evaluate:

Real system behavior.

Real failure behavior.

Real runtime behavior.

Real operational behavior.

---

Required test categories:

* integration tests
* workflow tests
* stress tests
* chaos tests
* load tests
* fault injection tests
* concurrency tests
* memory tests
* performance tests
* resilience tests
* recovery tests
* degradation tests

---

Every test must answer:

What happens in production?

Not:

Did assertion pass?

---

# STAGE 10.5 – BEHAVIORAL VALIDATION

Passing tests do not prove correctness.

Every subsystem must be evaluated under realistic operating conditions.

Validation must determine:

* whether outputs are reasonable
* whether predictions are stable
* whether system behavior is explainable
* whether resource usage is justified
* whether failure modes are controlled
* whether degradation behavior is acceptable

Focus on actual behavior.

Not only code coverage.

Not only assertion counts.

Not only test pass rates.


# STAGE 11 – REFACTOR PLAN CREATION

Only after analysis completes.

Create:

Phase 1
Phase 2
Phase 3
Phase 4
...

For every phase:

* objective
* risk
* dependencies
* expected outcome
* rollback strategy

No coding yet.

---

# STAGE 12 – IMPLEMENTATION RULES

After plan approval:

Only one file may be modified at a time.

Process:

Analyze File
Refactor File
Test File
Commit File

Repeat.

---

# COMMIT POLICY

Mandatory:

ONE FILE = ONE COMMIT

No multi-file commits.

No batch commits.

No large commits.

Every commit must:

* explain change
* explain reason
* explain impact

---

# TESTING BEFORE COMMIT

Before every commit:

Run:

* unit validation
* integration validation
* dependency validation
* regression validation

Commit only if:

behavior improves

and

no regressions detected

---

# PUSH POLICY

Pushing is forbidden until:

1. Analysis completed
2. Refactor completed
3. Testing completed
4. Validation completed

Push only after:

* system behaves correctly
* metrics improve
* behavior is validated
* production scenarios pass

---

# CODE QUALITY RULES

Forbidden:

* AI-generated filler code
* placeholder code
* speculative code
* unused abstractions
* unnecessary interfaces
* premature optimization
* fake extensibility
* overengineering



- AI-generated naming patterns
- generic helper abstractions
- speculative architecture
- framework-driven complexity
- unnecessary design patterns
- abstraction without measurable benefit
- configuration sprawl
- hidden coupling

Forbidden comments:

* TODO
* FIX LATER
* TEMP
* HACK
* QUICK FIX

Every line must exist for a reason.

---

# HUMANIZED REFACTOR RULE

Code must look like it was written by an experienced engineer.

Not by an AI.

Not by a code generator.

Not by a template.

Every abstraction must solve a real problem.

Every function must have a reason to exist.

Every file must justify its existence.

---

# FINAL DELIVERABLES

Produce:

1. Repository dependency graph

2. Folder-by-folder analysis report

3. File-by-file analysis report

4. Function-by-function analysis report

5. Dead code report

6. Unused code report

7. Hardcoded logic report

8. Magic number report

9. Math validation report

10. Physics validation report

11. Architecture review report

12. Enterprise testing report

13. Refactor roadmap

14. Risk assessment report

15. Implementation roadmap

16. Commit roadmap

17. Dependency graph report

18. Runtime execution-path report

19. Behavioral validation report

20. Mathematical model assessment

21. Physics model assessment

22. Architectural debt report

23. Production readiness scorecard

24. Prioritized implementation backlog

No code generation until all reports are complete and validated.
