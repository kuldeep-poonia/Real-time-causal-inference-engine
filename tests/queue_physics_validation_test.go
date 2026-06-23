package tests

import (
	"math"
	"testing"

	"absia/internal/intelligence/phase3_causal"
)

// TestQueueModelsParallelism validates the behavior of M/M/1 vs G/G/1 under various traffic profiles.
func TestQueueModelsParallelism(t *testing.T) {
	// We want to test different NodeStates
	cases := []struct {
		name         string
		lambda       float64
		mu           float64
		isOverloaded bool
		ca2          float64
		cs2          float64
		expectedMM1  float64 // queue length roughly
		expectKingmanGreater bool
	}{
		{
			name:         "Stable, M/M/1 Ideal (C=1)",
			lambda:       50,
			mu:           100,
			isOverloaded: false,
			ca2:          1.0,
			cs2:          1.0,
			expectedMM1:  1.0, // rho/(1-rho) = 0.5/0.5 = 1.0
			expectKingmanGreater: false, // Should be equal
		},
		{
			name:         "Stable, Highly Bursty Arrivals (C_A^2=4)",
			lambda:       50,
			mu:           100,
			isOverloaded: false,
			ca2:          4.0,
			cs2:          1.0,
			expectedMM1:  1.0, 
			expectKingmanGreater: true, // Burstiness amplifies queue
		},
		{
			name:         "Stable, Deterministic Service (C_S^2=0.1)",
			lambda:       50,
			mu:           100,
			isOverloaded: false,
			ca2:          1.0,
			cs2:          0.1,
			expectedMM1:  1.0,
			expectKingmanGreater: false, // Less variance -> smaller queue than M/M/1
		},
		{
			name:         "Overloaded, Deterministic Growth",
			lambda:       150,
			mu:           100,
			isOverloaded: true,
			ca2:          2.0,
			cs2:          2.0,
			expectedMM1:  50.0, // (150-100)*1 = 50
			expectKingmanGreater: false, // Both fallback to fluid accumulation
		},
	}

	mm1 := &phase3_causal.MM1QueueModel{}
	kingman := &phase3_causal.KingmanGG1QueueModel{}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := phase3_causal.NodeState{
				ArrivalRate: tc.lambda,
				ServiceRate: tc.mu,
				ArrivalCV2:  tc.ca2,
				ServiceCV2:  tc.cs2,
			}

			resMM1 := mm1.Calculate(tc.lambda, tc.mu, tc.isOverloaded, 0, state)
			resKingman := kingman.Calculate(tc.lambda, tc.mu, tc.isOverloaded, 0, state)

			t.Logf("[%s] M/M/1 Queue: %.2f | Kingman G/G/1 Queue: %.2f", tc.name, resMM1.QueueLength, resKingman.QueueLength)

			// Floating point comparison
			if math.Abs(resMM1.QueueLength-tc.expectedMM1) > 0.01 {
				t.Errorf("M/M/1 expected %.2f, got %.2f", tc.expectedMM1, resMM1.QueueLength)
			}

			if tc.expectKingmanGreater && resKingman.QueueLength <= resMM1.QueueLength {
				t.Errorf("Expected Kingman to predict larger queue due to burstiness. Kingman: %.2f, MM1: %.2f", resKingman.QueueLength, resMM1.QueueLength)
			}

			if !tc.expectKingmanGreater && tc.cs2 < 1.0 && resKingman.QueueLength >= resMM1.QueueLength {
				t.Errorf("Expected Kingman to predict smaller queue due to deterministic service. Kingman: %.2f, MM1: %.2f", resKingman.QueueLength, resMM1.QueueLength)
			}
			
			if tc.isOverloaded && resKingman.QueueLength != resMM1.QueueLength {
				t.Errorf("Expected identical deterministic fluid growth in overloaded state.")
			}
		})
	}
}

// BenchmarkQueueModelOverhead measures the computational overhead of evaluating multiple models simultaneously.
func BenchmarkQueueModelOverhead(b *testing.B) {
	state := phase3_causal.NodeState{
		ArrivalRate: 80,
		ServiceRate: 100,
		ArrivalCV2:  3.5,
		ServiceCV2:  2.1,
	}

	b.Run("MM1 Only", func(b *testing.B) {
		mm1 := &phase3_causal.MM1QueueModel{}
		for i := 0; i < b.N; i++ {
			_ = mm1.Calculate(80, 100, false, 0, state)
		}
	})

	b.Run("Dual Model (MM1 + Kingman)", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			for _, model := range phase3_causal.RegisteredQueueModels {
				_ = model.Calculate(80, 100, false, 0, state)
			}
		}
	})
}
