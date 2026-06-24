package bridge

import (
	"math"
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

// SelectSCMFunc picks an appropriate SCM structure based on node variance/density.
// This allows dynamic assignment while remaining pluggable.
func SelectSCMFunc(variance float64) SCMFunc {
	switch {
	case variance < 0.1:
		return Linear
	case variance < 0.5:
		return Polynomial
	case variance < 0.8:
		return Piecewise
	default:
		return Threshold // Highly volatile nodes treated as thresholds
	}
}
