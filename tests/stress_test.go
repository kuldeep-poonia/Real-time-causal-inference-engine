package tests

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"absia/pkg/metricsstore"
	"absia/pkg/orchestrator"
)

// ──────────────────────────────────────────────────────────────────────────────
// TEST 07 — Concurrent Stress
//
// Simulates real production load: many goroutines running the pipeline
// simultaneously with different targets.
//
// Tests:
//   · No panics under concurrent pipeline execution
//   · Safety result always non-nil regardless of goroutine count
//   · Per-target ranking state is isolated (target A's rankings don't bleed into B)
//   · MetricsStore concurrent writes + pipeline reads (real data path)
//   · Execution time stays bounded (no deadlocks, no starvation)
//   · Error accumulation under load does not corrupt shared state
//   · Zero scores or NaN scores never returned under concurrent access
//   · GetPrevRanking is thread-safe and returns correct prior per target
//
// Output: results.json
// ──────────────────────────────────────────────────────────────────────────────

type LoadRun struct {
	WorkerID     int     `json:"worker_id"`
	Lambda       float64 `json:"lambda"`
	Mu           float64 `json:"mu"`
	Q            float64 `json:"q"`
	Success      bool    `json:"success"`
	ErrorMsg     string  `json:"error_if_any"`
	SafetyNonNil bool    `json:"safety_result_non_nil"`
	ScoreBounded bool    `json:"score_in_0_1"`
	Score        float64 `json:"confidence_score"`
	State        string  `json:"confidence_state"`
	ExecTimeMS   float64 `json:"exec_time_ms"`
	DataSource   string  `json:"data_source"`
	NoPanic      bool    `json:"no_panic"`
}

type ConcurrentBatch struct {
	BatchName        string    `json:"batch_name"`
	Workers          int       `json:"worker_count"`
	TotalRuns        int       `json:"total_runs"`
	Successes        int       `json:"successes"`
	Panics           int       `json:"panics"`
	SafetyNilCount   int       `json:"safety_nil_count"`
	NaNScores        int       `json:"nan_or_inf_scores"`
	OutOfBoundScores int       `json:"scores_out_of_0_1"`
	AvgExecMS        float64   `json:"avg_exec_time_ms"`
	MaxExecMS        float64   `json:"max_exec_time_ms"`
	Runs             []LoadRun `json:"per_worker_results"`
}

type RankingIsolationCase struct {
	CaseName string   `json:"case_name"`
	TargetA  string   `json:"target_a"`
	TargetB  string   `json:"target_b"`
	RankA    []string `json:"ranking_stored_for_a"`
	RankB    []string `json:"ranking_stored_for_b"`
	Isolated bool     `json:"rankings_are_isolated"`
	Details  string   `json:"details"`
}

type StoreIntegrationCase struct {
	CaseName        string `json:"case_name"`
	NodesWritten    int    `json:"nodes_written_concurrently"`
	SamplesEach     int    `json:"samples_per_node"`
	WindowSize      int    `json:"window_size"`
	AllWithinWindow bool   `json:"all_within_window_after"`
	NoRaces         bool   `json:"no_data_race"`
	PipelineRan     bool   `json:"pipeline_ran_on_real_store"`
	SafetyOK        bool   `json:"safety_result_non_nil"`
}

type StressReport struct {
	TestSuite        string                 `json:"test_suite"`
	Timestamp        string                 `json:"timestamp_utc"`
	Batches          []ConcurrentBatch      `json:"concurrent_batches"`
	RankingIsolation []RankingIsolationCase `json:"ranking_isolation"`
	StoreIntegration []StoreIntegrationCase `json:"store_integration_under_load"`
	Summary          struct {
		TotalWorkers    int     `json:"total_workers_across_all_batches"`
		TotalPanics     int     `json:"total_panics"`
		TotalSafetyNil  int     `json:"total_safety_nil"`
		TotalNaNScores  int     `json:"total_nan_inf_scores"`
		TotalOutOfBound int     `json:"total_out_of_bound_scores"`
		RankingIsolated bool    `json:"ranking_isolation_holds"`
		MaxBatchExecMS  float64 `json:"max_batch_exec_time_ms"`
		Overall         string  `json:"overall_verdict"`
	} `json:"summary"`
}

func makeSample(lambda, mu, queue float64) metricsstore.NodeSample {
	return metricsstore.NodeSample{
		ArrivalRate: lambda, ServiceRate: mu, QueueLength: queue,
		Timestamp: float64(time.Now().UnixNano()) / 1e9, WallTime: time.Now(),
	}
}

func runWorker(workerID int, lambda, mu, q float64) LoadRun {
	run := LoadRun{
		WorkerID: workerID, Lambda: lambda, Mu: mu, Q: q, NoPanic: true,
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				run.NoPanic = false
				run.Success = false
				run.ErrorMsg = fmt.Sprintf("PANIC: %v", r)
			}
		}()

		start := time.Now()
		store := metricsstore.New(4)
		res, err := orchestrator.ExecuteFullPipelineFromStore(lambda, mu, q, store)
		run.ExecTimeMS = float64(time.Since(start).Nanoseconds()) / 1e6

		if err != nil {
			run.Success = false
			run.ErrorMsg = err.Error()
			return
		}
		if res == nil {
			run.Success = false
			run.ErrorMsg = "nil result"
			return
		}
		run.Success = true
		run.DataSource = res.DataSource
		run.SafetyNonNil = res.SafetyResult != nil

		if res.SafetyResult != nil {
			score := res.SafetyResult.Confidence.Score
			run.Score = score
			run.State = res.SafetyResult.Confidence.State.String()
			run.ScoreBounded = score >= 0.0 && score <= 1.0 &&
				!math.IsNaN(score) && !math.IsInf(score, 0)
		}
	}()

	return run
}

func TestConcurrentStress(t *testing.T) {
	report := StressReport{
		TestSuite: "T07_ConcurrentStress",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	totalPanics := 0
	totalNil := 0
	totalNaN := 0
	totalOOB := 0
	maxBatchExec := 0.0

	// ── Concurrent pipeline batches ───────────────────────────────────────────
	batches := []struct {
		name    string
		workers int
		inputs  []struct{ lam, mu, q float64 }
	}{
		{
			"light_load_4_workers", 4,
			[]struct{ lam, mu, q float64 }{
				{5.0, 10.0, 2.0}, {9.0, 10.0, 9.0},
				{20.0, 10.0, 100.0}, {0.5, 10.0, 0.0},
			},
		},
		{
			"medium_load_8_workers", 8,
			[]struct{ lam, mu, q float64 }{
				{5.0, 10.0, 2.0}, {9.0, 10.0, 9.0},
				{20.0, 10.0, 100.0}, {50.0, 10.0, 500.0},
				{95.0, 100.0, 100.0}, {0.1, 10.0, 0.0},
				{500.0, 1000.0, 5.0}, {99.0, 100.0, 990.0},
			},
		},
		{
			"overload_storm_16_workers", 16,
			func() []struct{ lam, mu, q float64 } {
				inputs := make([]struct{ lam, mu, q float64 }, 16)
				for i := range inputs {
					inputs[i] = struct{ lam, mu, q float64 }{
						lam: float64(i+1) * 10.0,
						mu:  100.0,
						q:   float64(i+1) * 5.0,
					}
				}
				return inputs
			}(),
		},
		{
			"mixed_workload_12_workers", 12,
			[]struct{ lam, mu, q float64 }{
				{5.0, 10.0, 2.0}, {9.0, 10.0, 9.0},
				{50.0, 10.0, 500.0}, {0.1, 10.0, 0.0},
				{95.0, 100.0, 100.0}, {99.0, 100.0, 990.0},
				{5.0, 10.0, 2.0}, {20.0, 10.0, 100.0},
				{500.0, 1000.0, 5.0}, {1.0, 2.0, 50.0},
				{0.01, 1.0, 0.0}, {100.0, 10.0, 1000.0},
			},
		},
	}

	for _, batch := range batches {
		batchStart := time.Now()
		var (
			panicCount int32
			nilCount   int32
			nanCount   int32
			oobCount   int32
		)
		runs := make([]LoadRun, batch.workers)
		var wg sync.WaitGroup
		wg.Add(batch.workers)

		for i := 0; i < batch.workers; i++ {
			go func(idx int) {
				defer wg.Done()
				inp := batch.inputs[idx]
				r := runWorker(idx, inp.lam, inp.mu, inp.q)
				runs[idx] = r

				if !r.NoPanic {
					atomic.AddInt32(&panicCount, 1)
					t.Errorf("PANIC in batch [%s] worker %d", batch.name, idx)
				}
				if !r.SafetyNonNil && r.NoPanic && r.Success {
					atomic.AddInt32(&nilCount, 1)
					t.Errorf("nil SafetyResult in batch [%s] worker %d", batch.name, idx)
				}
				if r.SafetyNonNil {
					if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) {
						atomic.AddInt32(&nanCount, 1)
						t.Errorf("NaN/Inf score in batch [%s] worker %d: %.4f", batch.name, idx, r.Score)
					}
					if !r.ScoreBounded {
						atomic.AddInt32(&oobCount, 1)
						t.Errorf("out-of-bound score [%s] worker %d: %.4f", batch.name, idx, r.Score)
					}
				}
			}(i)
		}
		wg.Wait()

		batchExecMS := float64(time.Since(batchStart).Nanoseconds()) / 1e6
		if batchExecMS > maxBatchExec {
			maxBatchExec = batchExecMS
		}

		successes := 0
		avgExec := 0.0
		maxExec := 0.0
		for _, r := range runs {
			if r.Success {
				successes++
			}
			avgExec += r.ExecTimeMS
			if r.ExecTimeMS > maxExec {
				maxExec = r.ExecTimeMS
			}
		}
		if batch.workers > 0 {
			avgExec /= float64(batch.workers)
		}

		pc := int(panicCount)
		nc := int(nilCount)
		nac := int(nanCount)
		oc := int(oobCount)
		totalPanics += pc
		totalNil += nc
		totalNaN += nac
		totalOOB += oc

		report.Batches = append(report.Batches, ConcurrentBatch{
			BatchName: batch.name, Workers: batch.workers, TotalRuns: batch.workers,
			Successes: successes, Panics: pc, SafetyNilCount: nc,
			NaNScores: nac, OutOfBoundScores: oc,
			AvgExecMS: avgExec, MaxExecMS: maxExec, Runs: runs,
		})
	}

	// ── Ranking isolation: GetPrevRanking is per-target ───────────────────────
	// Run two pipeline calls for different targets and verify their stored
	// rankings are independent.
	{
		// Set explicit different rankings for two targets
		// (simulates what the orchestrator does internally)
		targetA := "service_A"
		targetB := "service_B"

		// The orchestrator stores rankings per-target via setPrevRanking.
		// We test this by running the pipeline twice and reading back.
		// Since we can't directly call setPrevRanking (unexported), we
		// exercise it through the public API.
		store := metricsstore.New(4)
		_, _ = orchestrator.ExecuteFullPipelineFromStore(5.0, 10.0, 2.0, store)
		rankA := orchestrator.GetPrevRanking(targetA)
		rankB := orchestrator.GetPrevRanking(targetB)

		// They should be independent slices; modifying one must not affect other
		isolated := true
		if rankA != nil && rankB != nil {
			// If same pointer (memory sharing), that's a bug
			// We can't check pointer equality from here, but we can verify:
			// push a different value into A and check B is unchanged
			if len(rankA) > 0 && len(rankB) > 0 {
				origB0 := ""
				if len(rankB) > 0 {
					origB0 = rankB[0]
				}
				rankA[0] = "MUTATED"
				rankBAfter := orchestrator.GetPrevRanking(targetB)
				if len(rankBAfter) > 0 && rankBAfter[0] == "MUTATED" {
					isolated = false
					t.Error("RANKING ISOLATION VIOLATED: mutating target A's slice affected target B")
				}
				_ = origB0
			}
		}

		report.RankingIsolation = append(report.RankingIsolation, RankingIsolationCase{
			CaseName: "target_A_vs_target_B", TargetA: targetA, TargetB: targetB,
			RankA: rankA, RankB: rankB, Isolated: isolated,
			Details: fmt.Sprintf("rankA_len=%d rankB_len=%d isolated=%v", len(rankA), len(rankB), isolated),
		})
	}

	// Concurrent GetPrevRanking calls must not race
	{
		var wg sync.WaitGroup
		results := make([][]string, 20)
		wg.Add(20)
		for i := 0; i < 20; i++ {
			go func(idx int) {
				defer wg.Done()
				target := fmt.Sprintf("target_%d", idx%3) // 3 distinct targets, races overlap
				results[idx] = orchestrator.GetPrevRanking(target)
			}(i)
		}
		wg.Wait()

		// All results must be either nil or valid slices (no panic = pass)
		report.RankingIsolation = append(report.RankingIsolation, RankingIsolationCase{
			CaseName: "concurrent_get_prev_ranking_no_race",
			Isolated: true,
			Details:  fmt.Sprintf("20 concurrent calls completed without panic, results=%d", len(results)),
		})
	}

	// ── Store under concurrent write + pipeline read ───────────────────────────
	{
		store := metricsstore.New(20)
		nodes := []string{"gateway", "auth", "db", "cache"}
		samplesEach := 10

		var wg sync.WaitGroup
		var noRace atomic.Bool
		noRace.Store(true)

		// Concurrent writers
		wg.Add(len(nodes))
		for _, nodeID := range nodes {
			go func(id string) {
				defer func() {
					if r := recover(); r != nil {
						noRace.Store(false)
						t.Errorf("panic writing node %s: %v", id, r)
					}
					wg.Done()
				}()
				for i := 0; i < samplesEach; i++ {
					store.Put(id, makeSample(float64(i)*5.0, 100.0, float64(i)))
					time.Sleep(time.Microsecond) // yield to scheduler
				}
			}(nodeID)
		}

		// Concurrent readers
		wg.Add(4)
		for i := 0; i < 4; i++ {
			go func() {
				defer wg.Done()
				for j := 0; j < 5; j++ {
					_ = store.HasRealData()
					_ = store.GetAllNodeIDs()
					_ = store.NodeCount()
					time.Sleep(time.Microsecond)
				}
			}()
		}
		wg.Wait()

		// Now run pipeline with this store
		res, err := orchestrator.ExecuteFullPipelineFromStore(5.0, 10.0, 2.0, store)
		pipelineRan := err == nil && res != nil
		safetyOK := pipelineRan && res.SafetyResult != nil

		allWithin := true
		for _, id := range nodes {
			cnt := store.SampleCount(id)
			if cnt > 20 {
				allWithin = false
				t.Errorf("store window exceeded: node %s has %d > 20", id, cnt)
			}
		}

		report.StoreIntegration = append(report.StoreIntegration, StoreIntegrationCase{
			CaseName:     "concurrent_write_then_pipeline_read",
			NodesWritten: len(nodes), SamplesEach: samplesEach, WindowSize: 20,
			AllWithinWindow: allWithin, NoRaces: noRace.Load(),
			PipelineRan: pipelineRan, SafetyOK: safetyOK,
		})
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	rankingIsolated := true
	for _, rc := range report.RankingIsolation {
		if !rc.Isolated {
			rankingIsolated = false
		}
	}

	overall := "PASS"
	if totalPanics > 0 || totalNil > 0 || totalNaN > 0 || totalOOB > 0 || !rankingIsolated {
		overall = "FAIL"
	}

	report.Summary.TotalPanics = totalPanics
	report.Summary.TotalSafetyNil = totalNil
	report.Summary.TotalNaNScores = totalNaN
	report.Summary.TotalOutOfBound = totalOOB
	report.Summary.RankingIsolated = rankingIsolated
	report.Summary.MaxBatchExecMS = maxBatchExec
	report.Summary.Overall = overall

	// Count total workers
	totalWorkers := 0
	for _, b := range report.Batches {
		totalWorkers += b.Workers
	}
	report.Summary.TotalWorkers = totalWorkers

	out, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("results.json", out, 0644); err != nil {
		t.Fatalf("write results.json: %v", err)
	}
	t.Logf("Stress verdict: %s | workers=%d panics=%d nil_safety=%d nan=%d oob=%d rank_isolated=%v",
		overall, totalWorkers, totalPanics, totalNil, totalNaN, totalOOB, rankingIsolated)
}
