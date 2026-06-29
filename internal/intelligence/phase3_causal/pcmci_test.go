package phase3_causal

import (
	"testing"
	"math"
)

func TestPCMCI(t *testing.T) {
	engine := NewPCMCIEngine(nil)

	// Create perfect lag-1 correlation: Y_t = X_{t-1}
	x := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	y := []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9} // Y is shifted X

	corr, pVal := engine.RunMCI("X", "Y", x, y, 1)

	if math.IsNaN(corr) || corr < 0.99 {
		t.Errorf("Expected very high correlation, got %f", corr)
	}

	if pVal > 0.05 {
		t.Errorf("Expected significant p-value (<=0.05), got %f", pVal)
	}
}
