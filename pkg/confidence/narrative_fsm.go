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

// GenerateDecision deterministically maps statistical confidence and latent risk to a unified narrative.
func GenerateDecision(target string, score float64, latentRisk string, stats MCOutput, missingServices []string) DecisionOutcome {
	out := DecisionOutcome{
		Timeline:  []string{},
		Narrative: []string{},
	}

	// 1. Determine Operational State & Title based on target & score
	if score >= 0.75 {
		out.OperationalState = "DEGRADED" // Known root cause, under control
		out.IncidentTitle = fmt.Sprintf("Confirmed root cause in %s", target)
		out.FailureCategory = "Component Failure"
		out.AutomationPermission = true
	} else if score >= 0.45 {
		out.OperationalState = "UNSTABLE"
		out.IncidentTitle = fmt.Sprintf("Suspected issue in %s", target)
		out.FailureCategory = "Anomalous Behavior"
		out.AutomationPermission = false
	} else {
		out.OperationalState = "UNKNOWN"
		out.IncidentTitle = fmt.Sprintf("Investigation required near %s", target)
		out.FailureCategory = "Complex System Failure"
		out.AutomationPermission = false
	}

	// 2. Compute severity based on uncertainty bounds
	// High epistemic uncertainty increases perceived severity because it represents unknown-unknowns
	out.Severity = (1.0 - score) * 0.5 + stats.EpistemicUncert*0.5
	if out.Severity > 1.0 {
		out.Severity = 1.0
	}

	// 3. Construct Narrative Block
	out.Narrative = append(out.Narrative, fmt.Sprintf("What is happening: %s is the most likely origin.", target))

	if len(missingServices) > 0 {
		missingStr := strings.Join(missingServices, ", ")
		out.Narrative = append(out.Narrative, fmt.Sprintf("Observability Gap: Cannot reach high confidence because telemetry is missing for: %s.", missingStr))
		out.ConfidenceNarrative = fmt.Sprintf("I cannot safely act because %d services are unobserved. Please install OTel metrics on missing services.", len(missingServices))
		out.Timeline = append(out.Timeline, "Telemetry gap identified. Causal inference restricted to epistemic bounds.")
	} else {
		if score >= 0.75 {
			out.ConfidenceNarrative = fmt.Sprintf("High confidence (%.0f%%) achieved through converging Bayesian and Causal evidence. Automation is permitted.", score*100)
			out.Narrative = append(out.Narrative, "How it appears to be happening: Statistical evidence strongly isolates this component.")
			out.Timeline = append(out.Timeline, "Root cause isolated with high statistical confidence.")
		} else {
			out.ConfidenceNarrative = fmt.Sprintf("Confidence is too low (%.0f%%) for automated action. Human operator required.", score*100)
			out.Narrative = append(out.Narrative, fmt.Sprintf("How it appears to be happening: Evidence is fragmented. Epistemic uncertainty is high (%.2f).", stats.EpistemicUncert))
			out.Timeline = append(out.Timeline, "Confidence insufficient to isolate root cause.")
		}
	}

	// 4. Latent Risk Overrides
	if latentRisk == "HIGH" {
		out.OperationalState = "CRITICAL" // Unbounded risk
		out.AutomationPermission = false
		out.Narrative = append(out.Narrative, "Before acting: System exhibits HIGH latent risk. Severe safety violation detected.")
		out.Timeline = append(out.Timeline, "HIGH latent risk forced safety halt.")
	} else if latentRisk == "MEDIUM" {
		out.Narrative = append(out.Narrative, "Before acting: System exhibits MEDIUM latent risk. Proceed with caution.")
	}

	if out.AutomationPermission {
		out.Narrative = append(out.Narrative, "Recommended action: Proceed with automated remediation policies.")
	} else {
		out.Narrative = append(out.Narrative, "Recommended action: Pause automated fixes. Require operator intervention.")
	}

	return out
}
