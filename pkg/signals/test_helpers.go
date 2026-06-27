//go:build !production

package signals

func (ab *AdaptiveBaseline) recordSignalForTest(compute, memory, network float64) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	ab.computeStats.Update(compute)
	ab.memoryStats.Update(memory)
	ab.networkStats.Update(network)
}

func (ab *AdaptiveBaseline) seedCorrelationData(cpu, throttle []float64) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	ab.cpuFractions = cpu
	ab.throttleFractions = throttle
}
