package phase5_insight

import (
	"sort"
)

// RankActions ranks the what-if simulated outcomes based on Expected Utility (EU).
// EU = P(recovery) * Benefit - P(harm) * Cost
func RankActions(results []WhatIfResult, targetSeverity float64, topologyDescendantsCount int) []WhatIfResult {
	if len(results) == 0 {
		return results
	}

	// Benefit is proportional to the severity of the anomaly we are trying to fix (e.g., 0.0 to 1.0)
	// Cost (Blast Radius) is proportional to how many downstream services could be affected.
	// We normalize the descendants count (heuristically, max 100 for normalization).
	costFactor := float64(topologyDescendantsCount) / 10.0
	if costFactor > 1.0 {
		costFactor = 1.0
	}

	for i, res := range results {
		// Calculate Expected Utility
		// Von Neumann & Morgenstern (1944)
		pRecovery := res.RecoveryProbability
		
		// Inherent risk/harm probability of the action
		pHarm := 0.1
		if res.Risk == "medium" {
			pHarm = 0.3
		} else if res.Risk == "high" {
			pHarm = 0.6
		}

		eu := (pRecovery * targetSeverity) - (pHarm * costFactor)
		results[i].ExpectedUtility = eu
	}

	// Sort actions by EU descending
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].ExpectedUtility == results[j].ExpectedUtility {
			// Tie breaker: choose the one with shorter recovery time
			return results[i].EstimatedRecoveryMinutes < results[j].EstimatedRecoveryMinutes
		}
		return results[i].ExpectedUtility > results[j].ExpectedUtility
	})

	return results
}

// GetSafestAction returns the action with the highest Expected Utility.
func GetSafestAction(rankedResults []WhatIfResult) (map[string]string, bool) {
	if len(rankedResults) == 0 {
		return nil, false
	}
	
	best := rankedResults[0]
	
	why := "Highest recovery probability with lowest blast radius"
	if best.Risk == "high" {
		why = "Only viable option, despite high risk"
	}
	
	return map[string]string{
		"action": best.Action,
		"why":    why,
	}, true
}
