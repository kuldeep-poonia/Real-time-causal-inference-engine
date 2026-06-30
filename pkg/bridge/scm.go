package bridge

import (
	"math"

	phase4 "absia/internal/intelligence/phase4_explanation"
)

// SCMFunc represents a structural causal equation.
type SCMFunc func(inputs []float64, noise float64) float64

// Linear is the standard linear additive model.
func Linear(inputs []float64, noise float64) float64 {
	sum := 0.0
	for _, inp := range inputs {
		sum += inp
	}
	return sum + noise
}

// Polynomial returns a quadratic formulation (a*x^2 + b*x).
func Polynomial(inputs []float64, noise float64) float64 {
	sum := 0.0
	for _, inp := range inputs {
		// x + 0.1 * x^2
		sum += inp + 0.1*inp*inp
	}
	return sum + noise
}

// Logistic applies a sigmoid constraint to bound the effect.
func Logistic(inputs []float64, noise float64) float64 {
	sum := 0.0
	for _, inp := range inputs {
		sum += inp
	}
	// Sigmoid center at 0, steepness 1. Return range [0, 1] + noise
	return (1.0 / (1.0 + math.Exp(-sum))) + noise
}

// Threshold applies a step function. Below threshold 0.5 it has zero effect.
func Threshold(inputs []float64, noise float64) float64 {
	sum := 0.0
	for _, inp := range inputs {
		if inp > 0.5 {
			sum += inp
		}
	}
	return sum + noise
}

// Piecewise applies a piecewise linear function (different slopes based on input magnitude).
func Piecewise(inputs []float64, noise float64) float64 {
	sum := 0.0
	for _, inp := range inputs {
		if inp < 0.2 {
			sum += inp * 0.5
		} else if inp < 0.8 {
			sum += inp * 1.5 - 0.2
		} else {
			sum += inp * 0.5 + 0.6
		}
	}
	return sum + noise
}

// FitLinearSCM fits a linear structural causal model using Ridge Regression.
// It learns the coefficients (weights) for each parent based on the provided dataset.
func FitLinearSCM(parentIDs []string, targetID string, samples []phase4.Sample) SCMFunc {
	k := len(parentIDs)
	if k == 0 {
		return func(inputs []float64, noise float64) float64 {
			return noise
		}
	}

	n := len(samples)
	if n == 0 {
		return Linear
	}

	// Build X matrix (n x k+1) and y vector (n)
	// Last column is 1.0 (intercept)
	X := make([][]float64, n)
	y := make([]float64, n)
	for i, sample := range samples {
		X[i] = make([]float64, k+1)
		for j, pid := range parentIDs {
			X[i][j] = sample[pid]
		}
		X[i][k] = 1.0
		y[i] = sample[targetID]
	}

	// Compute X'X (k+1 x k+1)
	XtX := make([][]float64, k+1)
	for i := 0; i <= k; i++ {
		XtX[i] = make([]float64, k+1)
		for j := 0; j <= k; j++ {
			sum := 0.0
			for r := 0; r < n; r++ {
				sum += X[r][i] * X[r][j]
			}
			XtX[i][j] = sum
		}
		// Ridge penalty to prevent singular matrices
		XtX[i][i] += 1e-4
	}

	// Compute X'y (k+1)
	Xty := make([]float64, k+1)
	for i := 0; i <= k; i++ {
		sum := 0.0
		for r := 0; r < n; r++ {
			sum += X[r][i] * y[r]
		}
		Xty[i] = sum
	}

	// Solve XtX * beta = Xty using Gaussian Elimination
	beta := solveLinearSystem(XtX, Xty)

	// beta[0:k] are the coefficients, beta[k] is the intercept
	return func(inputs []float64, noise float64) float64 {
		sum := beta[k] // intercept
		for i, inp := range inputs {
			if i < k && i < len(inputs) {
				sum += beta[i] * inp
			}
		}
		return sum + noise
	}
}

// solveLinearSystem solves Ax = b using Gaussian elimination with partial pivoting.
// A is modified in-place.
func solveLinearSystem(A [][]float64, b []float64) []float64 {
	n := len(b)
	x := make([]float64, n)

	// Forward elimination
	for i := 0; i < n; i++ {
		maxEl := math.Abs(A[i][i])
		maxRow := i
		for k := i + 1; k < n; k++ {
			if math.Abs(A[k][i]) > maxEl {
				maxEl = math.Abs(A[k][i])
				maxRow = k
			}
		}

		A[i], A[maxRow] = A[maxRow], A[i]
		b[i], b[maxRow] = b[maxRow], b[i]

		for k := i + 1; k < n; k++ {
			if math.Abs(A[i][i]) < 1e-12 {
				continue // fallback for extremely singular matrices
			}
			c := -A[k][i] / A[i][i]
			for j := i; j < n; j++ {
				if i == j {
					A[k][j] = 0
				} else {
					A[k][j] += c * A[i][j]
				}
			}
			b[k] += c * b[i]
		}
	}

	// Back substitution
	for i := n - 1; i >= 0; i-- {
		if math.Abs(A[i][i]) < 1e-12 {
			x[i] = 0 // Fallback
			continue
		}
		x[i] = b[i] / A[i][i]
		for k := i - 1; k >= 0; k-- {
			b[k] -= A[k][i] * x[i]
		}
	}

	return x
}
