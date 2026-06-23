package api

import (
	"encoding/json"
	"net/http"

	"absia/pkg/orchestrator"
)

type ExploreRequest struct {
	Question string `json:"question"`
}

// ExploreHandler answers natural language questions about the incident deterministically.
func ExploreHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ExploreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	sem, confNarrative, title := cachedSemantics()
	if sem == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(orchestrator.ExploreAnswer{
			Question: req.Question,
			Answer:   "No service explanation is available yet. ABSIA is waiting for at least 4 samples per service.",
			Category: "no_data",
		})
		return
	}

	answer := orchestrator.ExploreQuestion(req.Question, sem, confNarrative, title)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)
}

type SemanticsResponse struct {
	OperationalState    string                           `json:"operational_state"`
	IncidentTitle       string                           `json:"incident_title"`
	FailureCategory     string                           `json:"failure_category"`
	RootCause           string                           `json:"root_cause,omitempty"`
	Target              string                           `json:"target,omitempty"`
	ConfidenceScore     float64                          `json:"confidence_score,omitempty"`
	ConfidenceState     string                           `json:"confidence_state,omitempty"`
	ConfidenceNarrative string                           `json:"confidence_narrative"`
	Severity            float64                          `json:"severity"`
	BlastRadius         int                              `json:"blast_radius"`
	Timeline            []string                         `json:"timeline,omitempty"`
	Narrative           []string                         `json:"narrative,omitempty"`
	Evidence            []orchestrator.FailureEvidence   `json:"evidence,omitempty"`
	Remediation         []orchestrator.RemediationAction `json:"remediation,omitempty"`
}

// SemanticsHandler returns the most recent FailureSemantics without triggering a pipeline run.
// This allows the UI to poll cheaply for operational state updates.
func SemanticsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sem, confNarrative, title := cachedSemantics()
	if sem == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SemanticsResponse{
			OperationalState:    "UNKNOWN",
			IncidentTitle:       "Waiting for service data...",
			ConfidenceNarrative: "ABSIA is gathering initial service samples.",
		})
		return
	}

	resp := SemanticsResponse{
		OperationalState:    string(sem.State),
		IncidentTitle:       title,
		FailureCategory:     string(sem.Category),
		RootCause:           sem.RootCause,
		Target:              sem.Target,
		ConfidenceScore:     sem.Confidence,
		ConfidenceState:     confidenceStateLabel(sem.Confidence),
		ConfidenceNarrative: confNarrative,
		Severity:            sem.Severity,
		BlastRadius:         sem.BlastRadius,
		Timeline:            sem.Timeline,
		Narrative:           orchestrator.OperationalNarrative(sem),
		Evidence:            sem.Evidence,
		Remediation:         sem.Remediation,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func confidenceStateLabel(score float64) string {
	switch {
	case score >= 0.75:
		return "Confident"
	case score >= 0.45:
		return "Needs review"
	default:
		return "Not sure yet"
	}
}
