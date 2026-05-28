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
			Answer:   "No causal intelligence data available yet. Waiting for enough telemetry samples to build the initial model (requires at least 4 samples per node).",
			Category: "no_data",
		})
		return
	}

	answer := orchestrator.ExploreQuestion(req.Question, sem, confNarrative, title)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(answer)
}

type SemanticsResponse struct {
	OperationalState    string                         `json:"operational_state"`
	IncidentTitle       string                         `json:"incident_title"`
	FailureCategory     string                         `json:"failure_category"`
	ConfidenceNarrative string                         `json:"confidence_narrative"`
	Severity            float64                        `json:"severity"`
	BlastRadius         int                            `json:"blast_radius"`
	Timeline            []string                       `json:"timeline,omitempty"`
	Narrative           []string                       `json:"narrative,omitempty"`
	Evidence            []orchestrator.FailureEvidence  `json:"evidence,omitempty"`
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
			IncidentTitle:       "Waiting for initial telemetry...",
			ConfidenceNarrative: "The system is gathering initial samples to build the causal graph.",
		})
		return
	}

	resp := SemanticsResponse{
		OperationalState:    string(sem.State),
		IncidentTitle:       title,
		FailureCategory:     string(sem.Category),
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
