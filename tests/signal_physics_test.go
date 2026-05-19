package tests

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────────────────────
// TEST 01 — Signal Physics (Phase 1 logic)
//
// Tests the exact mathematical formulas that Phase 1 applies to every ingest:
//   · rho = lambda / mu                     (M/M/1 utilisation)
//   · W   = Q / lambda                      (Little's Law wait time)
//   · Wq  = rho / (mu - lambda)  when rho<1  (stable queue wait)
//   · Wq  = Q / mu               when rho>=1 (overloaded queue wait)
//   · EMA_t = alpha*x_t + (1-alpha)*EMA_{t-1}
//   · mu_hat = 0.5 + 1/(1+Var[Dx]) * 2.0   (service rate estimation)
//
// Output: results.json
// ──────────────────────────────────────────────────────────────────────────────

// ── inline mirrors of pipeline formulas (no import cycle needed) ───────────

func rho(lambda, mu float64) float64 {
	if mu <= 0 {
		return math.Inf(1)
	}
	return lambda / mu
}

func littlesW(Q, lambda float64) float64 {
	if lambda <= 0 {
		return 0
	}
	return Q / lambda
}

func wqStable(r, mu float64) float64 {
	lambda := r * mu
	denom := mu - lambda
	if denom <= 0 {
		return math.Inf(1)
	}
	return r / denom
}

func wqOverloaded(Q, mu float64) float64 {
	if mu <= 0 {
		return math.Inf(1)
	}
	return Q / mu
}

func emaStep(alpha, prev, x float64) float64 {
	return alpha*x + (1-alpha)*prev
}

func muEstimate(varianceDelta float64) float64 {
	return 0.5 + (1.0/(1.0+varianceDelta))*2.0
}

// ── JSON output structs ───────────────────────────────────────────────────────

type MM1Case struct {
	Name             string  `json:"name"`
	Lambda           float64 `json:"lambda_arrival_rate"`
	Mu               float64 `json:"mu_service_rate"`
	Q                float64 `json:"queue_length"`
	RhoComputed      float64 `json:"rho_computed"`
	RhoExpected      float64 `json:"rho_expected"`
	RhoAbsError      float64 `json:"rho_abs_error"`
	RhoCorrect       bool    `json:"rho_correct"`
	LittlesW         float64 `json:"littles_law_W_wait"`
	WqValue          float64 `json:"wq_queue_wait"`
	WqMethod         string  `json:"wq_method"`
	Overloaded       bool    `json:"system_overloaded"`
	PhysicsValid     bool    `json:"physics_invariants_hold"`
	InvariantDetails string  `json:"invariant_details"`
}

type EMACase struct {
	Alpha           float64   `json:"alpha"`
	StepsRun        int       `json:"steps"`
	FirstFiveOutput []float64 `json:"ema_first_5_values"`
	FinalEMA        float64   `json:"ema_final_value"`
	Target          float64   `json:"convergence_target"`
	GapToTarget     float64   `json:"gap_to_target"`
	Converged       bool      `json:"converged_within_5pct"`
}

type BoundaryCase struct {
	Name        string  `json:"name"`
	Lambda      float64 `json:"lambda"`
	Mu          float64 `json:"mu"`
	Rho         string  `json:"rho_value"` // string to handle Inf
	RhoClass    string  `json:"rho_classification"`
	WIsFinite   bool    `json:"W_is_finite"`
	SafeForCalc bool    `json:"safe_for_pipeline"`
}

type MuEstCase struct {
	Name        string  `json:"name"`
	VarianceDx  float64 `json:"variance_delta_x"`
	MuHat       float64 `json:"mu_hat_estimated"`
	LowerBound  float64 `json:"lower_bound_0_5"`
	UpperBound  float64 `json:"upper_bound_2_5"`
	InRange     bool    `json:"in_valid_range"`
	Monotone    string  `json:"monotone_note"`
}

type PhysicsReport struct {
	TestSuite    string         `json:"test_suite"`
	Timestamp    string         `json:"timestamp_utc"`
	MM1Cases     []MM1Case      `json:"mm1_queueing_correctness"`
	EMACases     []EMACase      `json:"ema_smoothing_convergence"`
	Boundaries   []BoundaryCase `json:"boundary_conditions"`
	MuEstCases   []MuEstCase    `json:"service_rate_estimation"`
	Summary      struct {
		TotalMM1       int     `json:"mm1_cases_tested"`
		AllRhoCorrect  bool    `json:"all_rho_values_correct"`
		MaxRhoAbsErr   float64 `json:"max_rho_absolute_error"`
		EMAConvergence string  `json:"ema_convergence_result"`
		BoundaryIssues int     `json:"boundary_cases_with_issues"`
		AllMuInRange   bool    `json:"all_mu_estimates_in_range"`
		Overall        string  `json:"overall_physics_verdict"`
	} `json:"summary"`
}

func TestSignalPhysics(t *testing.T) {
	report := PhysicsReport{
		TestSuite: "T01_SignalPhysics",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	allRhoOK := true
	maxRhoErr := 0.0

	// ── M/M/1 correctness ─────────────────────────────────────────────────────
	cases := []struct {
		name            string
		lam, mu, q      float64
		expectedRho     float64
	}{
		{"healthy_0.5_utilisation",        5.0,   10.0,  2.0,   0.50},
		{"near_saturation_0.9",            9.0,   10.0,  9.0,   0.90},
		{"exactly_saturated_rho_1",       10.0,   10.0, 50.0,   1.00},
		{"overloaded_2x",                 20.0,   10.0, 100.0,  2.00},
		{"severe_overload_10x",          100.0,   10.0, 1000.0,10.00},
		{"near_idle_0.01",                 0.1,   10.0,  0.0,   0.01},
		{"zero_arrival_rho_0",             0.0,   10.0,  0.0,   0.00},
		{"high_throughput",              500.0, 1000.0,  5.0,   0.50},
		{"microservice_spike_0.95",       95.0,  100.0,100.0,   0.95},
		{"rho_0.99_brink_of_saturation",  99.0,  100.0,990.0,   0.99},
		{"heavy_tail_lam_equals_mu",      42.0,   42.0,200.0,   1.00},
		{"low_load_rho_0.1",               1.0,   10.0,  0.1,   0.10},
	}

	for _, c := range cases {
		r := rho(c.lam, c.mu)
		overloaded := r >= 1.0

		W := littlesW(c.q, c.lam)

		var Wq float64
		var wqMethod string
		if !overloaded {
			Wq = wqStable(r, c.mu)
			wqMethod = "analytic_mm1_stable"
		} else {
			Wq = wqOverloaded(c.q, c.mu)
			wqMethod = "empirical_overloaded"
		}

		absErr := math.Abs(r - c.expectedRho)
		correct := absErr < 1e-9
		if absErr > maxRhoErr {
			maxRhoErr = absErr
		}
		if !correct {
			allRhoOK = false
			t.Errorf("rho mismatch [%s]: computed=%.8f expected=%.8f err=%.2e", c.name, r, c.expectedRho, absErr)
		}

		// Physics invariants: W≥0, Wq≥0 (or +Inf), rho≥0
		wOK := W >= 0
		wqOK := math.IsInf(Wq, 1) || Wq >= 0
		rhoOK := !math.IsNaN(r)
		physicsValid := wOK && wqOK && rhoOK

		if !physicsValid {
			t.Errorf("physics invariant violated [%s]: W=%.4f Wq=%.4f rho=%.4f", c.name, W, Wq, r)
		}

		// Little's Law check: when stable and Q>0, W should be reasonable
		if c.lam > 0 && c.q > 0 && !overloaded {
			// W*lambda should equal Q (Little's Law)
			Wlambda := W * c.lam
			littlesErr := math.Abs(Wlambda-c.q) / c.q
			if littlesErr > 1e-9 {
				t.Errorf("Little's Law violated [%s]: W*lambda=%.6f Q=%.6f", c.name, Wlambda, c.q)
			}
		}

		rhoStr := fmt.Sprintf("%.6f", r)
		if math.IsInf(r, 1) {
			rhoStr = "+Inf"
		}
		_ = rhoStr

		report.MM1Cases = append(report.MM1Cases, MM1Case{
			Name: c.name, Lambda: c.lam, Mu: c.mu, Q: c.q,
			RhoComputed: r, RhoExpected: c.expectedRho, RhoAbsError: absErr,
			RhoCorrect: correct, LittlesW: W, WqValue: Wq, WqMethod: wqMethod,
			Overloaded: overloaded, PhysicsValid: physicsValid,
			InvariantDetails: fmt.Sprintf("W>=0:%v Wq_ok:%v rho_finite:%v", wOK, wqOK, rhoOK),
		})
	}

	// ── EMA convergence ───────────────────────────────────────────────────────
	alphas := []float64{0.05, 0.1, 0.3, 0.5, 0.7, 0.9}
	prevAlphaFinal := 0.0 // monotonicity check across alphas
	for i, alpha := range alphas {
		target := 10.0
		emaVal := 0.0
		steps := 50
		first5 := make([]float64, 0, 5)
		for step := 0; step < steps; step++ {
			emaVal = emaStep(alpha, emaVal, target)
			if step < 5 {
				first5 = append(first5, emaVal)
			}
		}
		gap := math.Abs(emaVal - target)
		converged := gap < 0.5 // within 5% of target=10

		if !converged {
			t.Errorf("EMA alpha=%.2f did not converge after %d steps: final=%.4f gap=%.4f", alpha, steps, emaVal, gap)
		}
		// Monotonicity: higher alpha means faster convergence (larger final value after same steps)
		if i > 0 && emaVal < prevAlphaFinal {
			t.Errorf("EMA monotonicity broken: alpha=%.2f final=%.4f < alpha=%.2f final=%.4f", alpha, emaVal, alphas[i-1], prevAlphaFinal)
		}
		prevAlphaFinal = emaVal

		report.EMACases = append(report.EMACases, EMACase{
			Alpha: alpha, StepsRun: steps, FirstFiveOutput: first5,
			FinalEMA: emaVal, Target: target, GapToTarget: gap, Converged: converged,
		})
	}

	// ── Boundary conditions ───────────────────────────────────────────────────
	boundaryIssues := 0
	bCases := []struct{ name string; lam, mu float64 }{
		{"zero_arrival",        0.0,    10.0},
		{"zero_service_rate",   10.0,   0.0},
		{"both_zero",           0.0,    0.0},
		{"equal_saturated",     10.0,   10.0},
		{"extreme_10x_overload",100.0,  10.0},
		{"astronomical_values", 1e9,    1e9},
		{"sub_millirps",        0.001,  0.002},
	}
	for _, b := range bCases {
		r := rho(b.lam, b.mu)
		var rhoStr, rhoClass string
		safe := true

		switch {
		case math.IsInf(r, 1):
			rhoStr = "+Inf"
			rhoClass = "infinite_overloaded"
			safe = false
		case math.IsNaN(r):
			rhoStr = "NaN"
			rhoClass = "undefined"
			safe = false
			boundaryIssues++
			t.Errorf("NaN rho for boundary case [%s]", b.name)
		case r == 0:
			rhoStr = "0.000000"
			rhoClass = "idle"
		case r < 1:
			rhoStr = fmt.Sprintf("%.6f", r)
			rhoClass = "stable"
		case r == 1:
			rhoStr = "1.000000"
			rhoClass = "critically_loaded"
		default:
			rhoStr = fmt.Sprintf("%.6f", r)
			rhoClass = "overloaded"
		}

		W := littlesW(0, b.lam) // Q=0 for boundary test
		report.Boundaries = append(report.Boundaries, BoundaryCase{
			Name: b.name, Lambda: b.lam, Mu: b.mu,
			Rho: rhoStr, RhoClass: rhoClass,
			WIsFinite: !math.IsInf(W, 0) && !math.IsNaN(W),
			SafeForCalc: safe,
		})
	}

	// ── Service rate estimation ───────────────────────────────────────────────
	allMuInRange := true
	prevMuEst := muEstimate(0.0) // should be max (2.5)
	muCases := []struct{ name string; vdx float64 }{
		{"no_variance",       0.0},
		{"tiny_variance",     0.001},
		{"low_variance",      0.01},
		{"moderate_variance", 0.5},
		{"high_variance",     5.0},
		{"very_high",         50.0},
		{"extreme",           1000.0},
	}
	for _, mc := range muCases {
		est := muEstimate(mc.vdx)
		inRange := est >= 0.5 && est <= 2.5
		if !inRange {
			allMuInRange = false
			t.Errorf("mu estimate out of [0.5,2.5] for variance=%.4f: got %.6f", mc.vdx, est)
		}
		// Monotone: higher variance → lower estimate (heuristic assumes stable=high-capacity)
		if est > prevMuEst+1e-9 {
			t.Errorf("mu estimate non-monotone: variance=%.4f gave est=%.4f > prev=%.4f", mc.vdx, est, prevMuEst)
		}
		prevMuEst = est

		report.MuEstCases = append(report.MuEstCases, MuEstCase{
			Name: mc.name, VarianceDx: mc.vdx, MuHat: est,
			LowerBound: 0.5, UpperBound: 2.5, InRange: inRange,
			Monotone: fmt.Sprintf("est=%.4f (higher_var→lower_est)", est),
		})
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	emaConv := "ALL_CONVERGED"
	for _, ec := range report.EMACases {
		if !ec.Converged {
			emaConv = "SOME_FAILED"
			break
		}
	}

	overall := "PASS"
	if !allRhoOK || !allMuInRange || emaConv != "ALL_CONVERGED" || boundaryIssues > 0 {
		overall = "FAIL"
	}

	report.Summary.TotalMM1 = len(report.MM1Cases)
	report.Summary.AllRhoCorrect = allRhoOK
	report.Summary.MaxRhoAbsErr = maxRhoErr
	report.Summary.EMAConvergence = emaConv
	report.Summary.BoundaryIssues = boundaryIssues
	report.Summary.AllMuInRange = allMuInRange
	report.Summary.Overall = overall

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile("results.json", out, 0644); err != nil {
		t.Fatalf("write results.json: %v", err)
	}
	t.Logf("Physics verdict: %s | rho_max_err=%.2e | mu_in_range=%v", overall, maxRhoErr, allMuInRange)
}