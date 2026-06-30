package confidence

import (
	"fmt"
	"strings"
)

// DecisionOutcome represents the strict, single-source-of-truth output of the decision engine.
type DecisionOutcome struct {
	OperationalState     string
	Severity             float64
	IncidentTitle        string
	FailureCategory      string
	ConfidenceNarrative  string
	Timeline             []string
	Narrative            []string
	AutomationPermission bool
}

// DecisionInput carries the pipeline signals that the FSM consumes.
// All narrative, state, and severity decisions are derived exclusively from these inputs.
type DecisionInput struct {
	Target          string   // The queried node (e.g., "frontend")
	RootCause       string   // Primary root cause identified by the pipeline
	Score           float64  // Confidence score from the safety gate
	LatentRisk      string   // "LOW", "MEDIUM", "HIGH"
	Dynamics        string   // System dynamics: "stable", "converging", "diverging", etc.
	Stats           MCOutput // Monte Carlo statistics
	MissingServices []string // Services with no telemetry
	FallbackUsed    bool     // Whether the system fell back to queue-physics
}

// GenerateDecision deterministically maps statistical confidence and latent risk to a unified narrative.
// This function is the SINGLE SOURCE OF TRUTH for all decision outputs.
// No other function should independently produce OperationalState, Severity, IncidentTitle, or Narrative.
func GenerateDecision(input DecisionInput) DecisionOutcome {
	out := DecisionOutcome{
		Timeline:  []string{},
		Narrative: []string{},
	}

	rootDisplay := input.RootCause
	if rootDisplay == "" {
		rootDisplay = input.Target
	}

	// 1. Determine Operational State & Title from score and dynamics
	switch {
	case input.Score >= 0.75:
		out.OperationalState = "DEGRADED"
		out.IncidentTitle = fmt.Sprintf("Confirmed root cause: %s", rootDisplay)
		out.FailureCategory = "Component Failure"
		out.AutomationPermission = true
	case input.Score >= 0.45:
		out.OperationalState = "UNSTABLE"
		out.IncidentTitle = fmt.Sprintf("%s needs investigation", rootDisplay)
		out.FailureCategory = "Anomalous Behavior"
		out.AutomationPermission = false
	default:
		out.OperationalState = "UNKNOWN"
		out.IncidentTitle = fmt.Sprintf("Investigation required near %s", input.Target)
		out.FailureCategory = "Complex System Failure"
		out.AutomationPermission = false
	}

	// Refine state based on dynamics
	switch input.Dynamics {
	case "converging":
		if out.OperationalState == "UNSTABLE" || out.OperationalState == "UNKNOWN" {
			out.OperationalState = "RECOVERING"
			out.IncidentTitle = fmt.Sprintf("%s is recovering", input.Target)
		}
	case "diverging":
		if out.OperationalState != "DEGRADED" {
			out.OperationalState = "UNSTABLE"
		}
	case "stable":
		if input.Score >= 0.45 && input.Score < 0.75 {
			out.OperationalState = "HEALTHY"
			out.IncidentTitle = "All checked services look healthy"
		}
	}

	// 2. Compute severity from uncertainty bounds
	out.Severity = (1.0 - input.Score) * 0.5
	if input.Stats.EpistemicUncert > 0 {
		out.Severity += input.Stats.EpistemicUncert * 0.5
	}
	if out.Severity > 1.0 {
		out.Severity = 1.0
	}

	// 3. Construct Narrative
	if input.RootCause != "" && input.RootCause != input.Target {
		out.Narrative = append(out.Narrative,
			fmt.Sprintf("What is happening: %s is the service most likely causing the issue affecting %s.", input.RootCause, input.Target))
	} else {
		out.Narrative = append(out.Narrative,
			fmt.Sprintf("What is happening: %s is under analysis.", input.Target))
	}

	// Dynamics-based explanation
	switch input.Dynamics {
	case "converging":
		out.Narrative = append(out.Narrative, "Why: recent pressure is going down instead of spreading.")
		out.Narrative = append(out.Narrative, "How to proceed: keep monitoring until the service stays stable.")
	case "diverging":
		out.Narrative = append(out.Narrative,
			fmt.Sprintf("Why it matters: %s is receiving more work or holding more backlog than it can comfortably process.", rootDisplay))
	case "stable":
		if out.OperationalState == "HEALTHY" {
			out.Narrative = append(out.Narrative, "Why: incoming work is staying within available capacity.")
			out.Narrative = append(out.Narrative, "How to proceed: keep monitoring; no fix is needed.")
		}
	}

	// Observability gaps
	if len(input.MissingServices) > 0 {
		missingStr := strings.Join(input.MissingServices, ", ")
		out.Narrative = append(out.Narrative,
			fmt.Sprintf("Observability gap: telemetry is missing for: %s.", missingStr))
		out.ConfidenceNarrative = fmt.Sprintf(
			"I cannot safely name one cause yet because most observed services are not represented in the current service map. Add service data for the missing services, then run the check again.")
		out.Timeline = append(out.Timeline, "Telemetry gap identified. Causal inference restricted to epistemic bounds.")
	} else if input.FallbackUsed {
		out.ConfidenceNarrative = fmt.Sprintf(
			"I am %.0f%% sure. Review the suggested steps before acting.", input.Score*100)
		out.Timeline = append(out.Timeline, "Causal graph did not produce a direct hypothesis; using backpressure inference.")
	} else if input.Score >= 0.75 {
		out.ConfidenceNarrative = fmt.Sprintf(
			"High confidence (%.0f%%) achieved through converging Bayesian and causal evidence. Automation is permitted.", input.Score*100)
		out.Timeline = append(out.Timeline, "Root cause isolated with high statistical confidence.")
	} else {
		out.ConfidenceNarrative = fmt.Sprintf(
			"I am %.0f%% sure. Review the suggested steps before acting.", input.Score*100)
		out.Timeline = append(out.Timeline, "Confidence insufficient to isolate root cause.")
	}

	// Timeline entry for the target
	out.Timeline = append(out.Timeline, fmt.Sprintf("%s: selected service may be affected by this issue.", input.Target))

	// 4. Latent Risk Overrides (unconditional — HIGH always forces CRITICAL)
	if input.LatentRisk == "HIGH" {
		out.OperationalState = "CRITICAL"
		out.AutomationPermission = false
		out.Narrative = append(out.Narrative, "Before acting: system exhibits HIGH latent risk. Severe safety violation detected.")
		out.Timeline = append(out.Timeline, "HIGH latent risk forced safety halt.")
	} else if input.LatentRisk == "MEDIUM" {
		out.Narrative = append(out.Narrative, "Before acting: system exhibits MEDIUM latent risk. Proceed with caution.")
	}

	// 5. Final action recommendation
	if out.AutomationPermission {
		out.Narrative = append(out.Narrative, "Recommended action: proceed with automated remediation policies.")
	} else {
		out.Narrative = append(out.Narrative, "Recommended action: pause automated fixes. Require operator intervention.")
	}

	return out
}
