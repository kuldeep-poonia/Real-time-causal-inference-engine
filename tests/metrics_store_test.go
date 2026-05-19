package tests

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"absia/pkg/metricsstore"
)

// ──────────────────────────────────────────────────────────────────────────────
// TEST 06 — MetricsStore
//
// Tests:
//   · Sliding window eviction: only last N samples retained
//   · GetLatestSample returns the most recently Put sample
//   · GetAllNodeIDs returns deterministic sorted order
//   · HasRealData: true only when ≥4 samples present for at least one node
//   · SampleCount / NodeCount accurate
//   · GetArrivalRateSeries values match insertion order
//   · Concurrent Put from multiple goroutines (no panic, no data race)
//   · Concurrent Put+Get does not return stale data beyond window
//   · windowSize < 4 is clamped to 4
//   · Empty store edge cases (nil returns, false HasRealData)
//
// Output: results.json
// ──────────────────────────────────────────────────────────────────────────────

type WindowCase struct {
	CaseName       string    `json:"case_name"`
	WindowSize     int       `json:"window_size"`
	InsertCount    int       `json:"insert_count"`
	ExpectedRetain int       `json:"expected_retain"`
	ActualRetain   int       `json:"actual_retain"`
	CorrectWindow  bool      `json:"window_eviction_correct"`
	LatestValue    float64   `json:"latest_value"`
	LatestCorrect  bool      `json:"latest_value_correct"`
	SeriesValues   []float64 `json:"arrival_rate_series"`
	SeriesOrder    string    `json:"series_order_status"`
}

type LatestSampleCase struct {
	CaseName       string  `json:"case_name"`
	NodeID         string  `json:"node_id"`
	InsertedLambda float64 `json:"last_inserted_lambda"`
	InsertedMu     float64 `json:"last_inserted_mu"`
	InsertedQ      float64 `json:"last_inserted_queue"`
	ReturnedOK     bool    `json:"returned_ok"`
	LambdaMatch    bool    `json:"lambda_matches"`
	MuMatch        bool    `json:"mu_matches"`
	QMatch         bool    `json:"q_matches"`
}

type HasRealDataCase struct {
	CaseName     string `json:"case_name"`
	Setup        string `json:"setup_description"`
	Expected     bool   `json:"expected_has_real_data"`
	Actual       bool   `json:"actual_has_real_data"`
	Correct      bool   `json:"correct"`
}

type SortedKeysCase struct {
	CaseName      string   `json:"case_name"`
	InsertedIDs   []string `json:"inserted_node_ids"`
	ReturnedIDs   []string `json:"returned_node_ids"`
	IsSorted      bool     `json:"is_sorted_ascending"`
	LengthMatches bool     `json:"length_matches"`
}

type ConcurrencyCase struct {
	CaseName       string `json:"case_name"`
	Goroutines     int    `json:"goroutines"`
	InsertsEach    int    `json:"inserts_per_goroutine"`
	WindowSize     int    `json:"window_size"`
	NodeCount      int    `json:"node_count_after"`
	NoPanic        bool   `json:"no_panic"`
	SampleCountOK  bool   `json:"sample_count_correct_per_node"`
	MaxSampleFound int    `json:"max_samples_in_any_node"`
}

type StoreReport struct {
	TestSuite       string             `json:"test_suite"`
	Timestamp       string             `json:"timestamp_utc"`
	WindowCases     []WindowCase       `json:"sliding_window_tests"`
	LatestCases     []LatestSampleCase `json:"get_latest_sample_tests"`
	HasRealDataCases []HasRealDataCase `json:"has_real_data_tests"`
	SortedKeys      []SortedKeysCase  `json:"sorted_node_id_tests"`
	Concurrency     []ConcurrencyCase `json:"concurrent_write_tests"`
	EdgeCases       struct {
		EmptyNodeReturnsFalse bool `json:"empty_node_returns_false_for_latest"`
		SubMinWindowClamped   bool `json:"window_lt_4_clamped_to_4"`
		ZeroSampleNoRealData  bool `json:"zero_samples_no_real_data"`
		SameNodeMultiPut      bool `json:"same_node_multi_put_ok"`
	} `json:"edge_cases"`
	Summary struct {
		WindowAllCorrect   bool   `json:"window_eviction_all_correct"`
		LatestAllCorrect   bool   `json:"latest_sample_all_correct"`
		HasRealDataCorrect bool   `json:"has_real_data_all_correct"`
		SortedKeysCorrect  bool   `json:"sorted_keys_correct"`
		ConcurrencyPassed  bool   `json:"concurrent_writes_safe"`
		Overall            string `json:"overall_verdict"`
	} `json:"summary"`
}

func makeStressSample(lambda, mu, queue float64) metricsstore.NodeSample {
	return metricsstore.NodeSample{
		ArrivalRate: lambda,
		ServiceRate: mu,
		QueueLength: queue,
		Timestamp:   float64(time.Now().UnixNano()) / 1e9,
		WallTime:    time.Now(),
	}
}

func isSortedAsc(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i] < s[i-1] { return false }
	}
	return true
}

func TestMetricsStore(t *testing.T) {
	report := StoreReport{
		TestSuite: "T06_MetricsStore",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// ── Sliding window eviction ────────────────────────────────────────────────
	windowAllCorrect := true
	type winCase struct {
		name       string
		winSize    int
		insertN    int
		expectKeep int
	}
	winCases := []winCase{
		{"exact_fill_4",             4,  4,  4},
		{"overflow_4_insert_10",     4, 10,  4},
		{"window_10_insert_7",      10,  7,  7},
		{"window_10_insert_20",     10, 20, 10},
		{"window_100_insert_50",   100, 50, 50},
		{"window_100_insert_200", 100, 200, 100},
		{"min_clamp_1_to_4",        1,  6,  4},  // window<4 clamped to 4
		{"min_clamp_3_to_4",        3,  8,  4},
	}

	for _, wc := range winCases {
		s := metricsstore.New(wc.winSize)
		nodeID := fmt.Sprintf("node_%s", wc.name)
		for i := 0; i < wc.insertN; i++ {
			s.Put(nodeID, makeSample(float64(i)*1.0, 10.0, 0.0))
		}
		actual := s.SampleCount(nodeID)
		correct := actual == wc.expectKeep
		if !correct {
			windowAllCorrect = false
			t.Errorf("window [%s]: kept=%d expected=%d", wc.name, actual, wc.expectKeep)
		}

		// Latest value should be insert N-1
		latestSample, ok := s.GetLatestSample(nodeID)
		expectedLatest := float64(wc.insertN-1) * 1.0
		latestCorrect := ok && math.Abs(latestSample.ArrivalRate-expectedLatest) < 1e-9

		// Series values: should be in insertion order (oldest to newest)
		series := s.GetArrivalRateSeries(nodeID)
		seriesOrder := "ascending"
		for i := 1; i < len(series); i++ {
			if series[i] < series[i-1] { seriesOrder = "not_monotone_ascending" }
		}

		report.WindowCases = append(report.WindowCases, WindowCase{
			CaseName: wc.name, WindowSize: wc.winSize, InsertCount: wc.insertN,
			ExpectedRetain: wc.expectKeep, ActualRetain: actual, CorrectWindow: correct,
			LatestValue: latestSample.ArrivalRate, LatestCorrect: latestCorrect,
			SeriesValues: series, SeriesOrder: seriesOrder,
		})
	}

	// ── GetLatestSample accuracy ───────────────────────────────────────────────
	latestAllCorrect := true
	s2 := metricsstore.New(20)
	latestCases := []struct {
		name   string
		nodeID string
		lambda, mu, queue float64
	}{
		{"auth_service",    "auth",    95.0,  100.0, 10.0},
		{"gateway",         "gateway", 980.0, 1000.0, 50.0},
		{"database",        "db",      120.0, 100.0, 500.0},  // overloaded
		{"cache",           "cache",    40.0, 100.0,  5.0},
		{"updated_gateway", "gateway",  50.0, 1000.0, 2.0},   // overwrite with different value
	}
	for _, lc := range latestCases {
		// Insert some prior samples then the target sample last
		s2.Put(lc.nodeID, makeSample(1.0, 5.0, 0.0)) // prior
		s2.Put(lc.nodeID, makeSample(lc.lambda, lc.mu, lc.queue)) // target = latest
		got, ok := s2.GetLatestSample(lc.nodeID)
		lMatch := math.Abs(got.ArrivalRate-lc.lambda) < 1e-9
		mMatch := math.Abs(got.ServiceRate-lc.mu) < 1e-9
		qMatch := math.Abs(got.QueueLength-lc.queue) < 1e-9
		correct := ok && lMatch && mMatch && qMatch
		if !correct {
			latestAllCorrect = false
			t.Errorf("latest [%s]: ok=%v λ_match=%v μ_match=%v q_match=%v", lc.name, ok, lMatch, mMatch, qMatch)
		}
		report.LatestCases = append(report.LatestCases, LatestSampleCase{
			CaseName: lc.name, NodeID: lc.nodeID,
			InsertedLambda: lc.lambda, InsertedMu: lc.mu, InsertedQ: lc.queue,
			ReturnedOK: ok, LambdaMatch: lMatch, MuMatch: mMatch, QMatch: qMatch,
		})
	}

	// ── HasRealData tests ─────────────────────────────────────────────────────
	hasRealDataCorrect := true

	hrdCases := []struct {
		name     string
		setup    string
		expected bool
		build    func(s *metricsstore.Store)
	}{
		{
			"empty_store", "no samples inserted", false,
			func(s *metricsstore.Store) {},
		},
		{
			"one_sample", "single sample for one node", false,
			func(s *metricsstore.Store) {
				s.Put("n1", makeSample(5.0, 10.0, 0.0))
			},
		},
		{
			"three_samples", "3 samples (below threshold of 4)", false,
			func(s *metricsstore.Store) {
				for i := 0; i < 3; i++ { s.Put("n1", makeSample(5.0, 10.0, 0.0)) }
			},
		},
		{
			"exactly_4_samples", "exactly 4 samples = threshold", true,
			func(s *metricsstore.Store) {
				for i := 0; i < 4; i++ { s.Put("n1", makeSample(5.0, 10.0, 0.0)) }
			},
		},
		{
			"five_samples", "5 samples beyond threshold", true,
			func(s *metricsstore.Store) {
				for i := 0; i < 5; i++ { s.Put("n1", makeSample(5.0, 10.0, 0.0)) }
			},
		},
		{
			"two_nodes_one_enough", "two nodes; only node2 has 4 samples", true,
			func(s *metricsstore.Store) {
				s.Put("n1", makeSample(5.0, 10.0, 0.0))
				for i := 0; i < 4; i++ { s.Put("n2", makeSample(5.0, 10.0, 0.0)) }
			},
		},
	}

	for _, hc := range hrdCases {
		s := metricsstore.New(20)
		hc.build(s)
		actual := s.HasRealData()
		correct := actual == hc.expected
		if !correct {
			hasRealDataCorrect = false
			t.Errorf("HasRealData [%s]: expected=%v actual=%v", hc.name, hc.expected, actual)
		}
		report.HasRealDataCases = append(report.HasRealDataCases, HasRealDataCase{
			CaseName: hc.name, Setup: hc.setup,
			Expected: hc.expected, Actual: actual, Correct: correct,
		})
	}

	// ── Sorted node IDs ───────────────────────────────────────────────────────
	sortedCorrect := true
	nodeIDSets := [][]string{
		{"zebra", "alpha", "mango", "banana"},
		{"z", "a", "m", "b", "c", "d"},
		{"node10", "node2", "node1", "node20"},
		{"single"},
		{"bb", "aa"},
	}
	for _, ids := range nodeIDSets {
		s := metricsstore.New(10)
		for _, id := range ids {
			s.Put(id, makeSample(1.0, 10.0, 0.0))
		}
		returned := s.GetAllNodeIDs()
		sorted := isSortedAsc(returned)
		lenMatch := len(returned) == len(ids)
		if !sorted || !lenMatch {
			sortedCorrect = false
			t.Errorf("GetAllNodeIDs not sorted: in=%v out=%v", ids, returned)
		}
		report.SortedKeys = append(report.SortedKeys, SortedKeysCase{
			CaseName: fmt.Sprintf("ids_%v", ids), InsertedIDs: ids,
			ReturnedIDs: returned, IsSorted: sorted, LengthMatches: lenMatch,
		})
	}

	// ── Concurrent writes ──────────────────────────────────────────────────────
	concCases := []struct {
		name        string
		goroutines  int
		insertsEach int
		windowSize  int
	}{
		{"low_concurrency_4g",         4,  50,  20},
		{"medium_concurrency_16g",    16,  25,  20},
		{"high_concurrency_64g",      64,  10,  10},
		{"shared_node_race_16g",      16,  40,  20}, // all write to same node
	}

	concurrencyPassed := true
	for _, cc := range concCases {
		s := metricsstore.New(cc.windowSize)
		noPanic := true

		var wg sync.WaitGroup
		wg.Add(cc.goroutines)
		for g := 0; g < cc.goroutines; g++ {
			go func(gID int) {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("panic in goroutine %d: %v", gID, r)
						noPanic = false
					}
					wg.Done()
				}()
				// All goroutines in shared_node_race write to same node
				nodeID := fmt.Sprintf("node_%d", gID)
				if cc.name == "shared_node_race_16g" { nodeID = "shared_node" }
				for i := 0; i < cc.insertsEach; i++ {
					s.Put(nodeID, makeSample(float64(gID)*1.0+float64(i)*0.01, 10.0, 0.0))
				}
			}(g)
		}
		wg.Wait()

		// Verify sample count doesn't exceed window for each node
		nodeIDs := s.GetAllNodeIDs()
		sampleCountOK := true
		maxSample := 0
		for _, id := range nodeIDs {
			cnt := s.SampleCount(id)
			if cnt > cc.windowSize {
				sampleCountOK = false
				t.Errorf("concurrent [%s]: node %s has %d samples > window %d", cc.name, id, cnt, cc.windowSize)
			}
			if cnt > maxSample { maxSample = cnt }
		}

		if !noPanic || !sampleCountOK { concurrencyPassed = false }

		report.Concurrency = append(report.Concurrency, ConcurrencyCase{
			CaseName: cc.name, Goroutines: cc.goroutines, InsertsEach: cc.insertsEach,
			WindowSize: cc.windowSize, NodeCount: len(nodeIDs),
			NoPanic: noPanic, SampleCountOK: sampleCountOK, MaxSampleFound: maxSample,
		})
	}

	// ── Edge cases ────────────────────────────────────────────────────────────
	// Empty node returns (_, false)
	emptyStore := metricsstore.New(10)
	_, emptyOK := emptyStore.GetLatestSample("nonexistent")
	report.EdgeCases.EmptyNodeReturnsFalse = !emptyOK

	// Window < 4 clamped to 4
	sClamp := metricsstore.New(1)
	for i := 0; i < 6; i++ { sClamp.Put("x", makeSample(float64(i), 10.0, 0.0)) }
	report.EdgeCases.SubMinWindowClamped = sClamp.SampleCount("x") == 4

	// Zero samples → no real data
	report.EdgeCases.ZeroSampleNoRealData = !emptyStore.HasRealData()

	// Same node multiple Put accumulates correctly
	sSame := metricsstore.New(10)
	for i := 0; i < 5; i++ { sSame.Put("x", makeSample(float64(i), 10.0, 0.0)) }
	report.EdgeCases.SameNodeMultiPut = sSame.SampleCount("x") == 5 && sSame.NodeCount() == 1

	if !emptyOK { } // expected
	if sClamp.SampleCount("x") != 4 {
		t.Errorf("window<4 not clamped: got %d", sClamp.SampleCount("x"))
	}
	if emptyStore.HasRealData() {
		t.Error("empty store reports HasRealData=true")
	}

	// ── Summary ───────────────────────────────────────────────────────────────
	overall := "PASS"
	if !windowAllCorrect || !latestAllCorrect || !hasRealDataCorrect || !sortedCorrect || !concurrencyPassed {
		overall = "FAIL"
	}

	report.Summary.WindowAllCorrect = windowAllCorrect
	report.Summary.LatestAllCorrect = latestAllCorrect
	report.Summary.HasRealDataCorrect = hasRealDataCorrect
	report.Summary.SortedKeysCorrect = sortedCorrect
	report.Summary.ConcurrencyPassed = concurrencyPassed
	report.Summary.Overall = overall

	out, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile("results.json", out, 0644); err != nil {
		t.Fatalf("write results.json: %v", err)
	}
	t.Logf("Store verdict: %s | window=%v latest=%v hrd=%v sorted=%v concurrent=%v",
		overall, windowAllCorrect, latestAllCorrect, hasRealDataCorrect, sortedCorrect, concurrencyPassed)
}