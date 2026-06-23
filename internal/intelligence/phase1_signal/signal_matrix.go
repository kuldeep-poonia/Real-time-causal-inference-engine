package phase1_signal

import (
	"encoding/json"
	"errors"
	"math"
	"sync"
)


// NodeState: physics state variables.  Consumed by Phase 3 propagation engine.
type NodeState struct {
	Load            float64 // ρ = λ/μ  (≥1 → overloaded)
	ArrivalRate     float64 // λ
	ServiceRate     float64 // μ
	QueueLength     float64 // L
	ProcessingDelay float64 // W  (seconds)
	Timestamp       float64 // last observation time

	ArrivalCV2      float64 // C_A^2 (Arrival Coefficient of Variation Squared)
	ServiceCV2      float64 // C_S^2 (Service Coefficient of Variation Squared)

	SignalIntensity float64
}

/*
SCHEMA
*/
type SignalInput struct {
	SignalID string
	GroupID  string
	Time     float64
	Values   map[string]float64
}

type SignalSchema struct {
	Features []string
	index    map[string]int
}

func NewSignalSchema(features []string) *SignalSchema {
	idx := make(map[string]int)
	for i, f := range features {
		idx[f] = i
	}
	return &SignalSchema{Features: features, index: idx}
}

func copyIndex(src map[string]int) map[string]int {
	dst := make(map[string]int)
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

/*
WELFORD STATS
*/
type Stats struct {
	Mean []float64
	M2   []float64
	N    int64
}

func (s *Stats) Update(x []float64) {
	s.N++
	if s.Mean == nil {
		s.Mean = append([]float64{}, x...)
		s.M2 = make([]float64, len(x))
		return
	}
	for i := range x {
		delta := x[i] - s.Mean[i]
		s.Mean[i] += delta / float64(s.N)
		s.M2[i] += delta * (x[i] - s.Mean[i])
	}
}

func (s *Stats) Std() []float64 {
	out := make([]float64, len(s.Mean))
	if s.N < 2 {
		for i := range out {
			out[i] = 1
		}
		return out
	}
	for i := range out {
		out[i] = math.Sqrt(s.M2[i] / float64(s.N-1))
	}
	return out
}

/*
POINT
*/
type SignalPoint struct {
	Tick int64
	T    float64
	V    []float64
	D    []float64
	A    []float64

	isReal bool
}

/*
MATRIX
*/
type Matrix struct {
	Time          []float64
	Values        [][]float64
	Derivatives   [][]float64
	RawValues     [][]float64
	Accelerations [][]float64
	FeatureIdx    map[string]int
}

/*
PROCESSOR — extended with arrival-time and derivative-magnitude tracking.
*/
type Processor struct {
	schema   *SignalSchema
	interval float64
	alpha    float64
	window   int

	lastPoint *SignalPoint
	lastEMA   []float64

	stats Stats
	matrix Matrix

	inputCh chan SignalInput
	mu      sync.Mutex
	groupID string

	// NEW: inter-arrival ring buffer → λ
	recentInterArrivals []float64
	lastIngestTime      float64
	maxArrivalWindow    int

	// NEW: derivative-magnitude ring buffer → μ
	

	lastIntensity float64
}

/*
INIT
*/
func NewProcessor(schema *SignalSchema, interval float64, window int, alpha float64, groupID string) *Processor {
	p := &Processor{
		schema:           schema,
		interval:         interval,
		window:           window,
		inputCh:          make(chan SignalInput, 1000),
		alpha:            alpha,
		groupID:          groupID,
		maxArrivalWindow: 30,
		matrix: Matrix{
			Time:          []float64{},
			Values:        [][]float64{},
			Derivatives:   [][]float64{},
			Accelerations: [][]float64{},
			FeatureIdx:    copyIndex(schema.index),
		},
	}
	go p.worker()
	return p
}

/*
ASYNC WORKER
*/
func (p *Processor) worker() {
	for in := range p.inputCh {
		p.process(in)
	}
}

func (p *Processor) Ingest(in SignalInput) error {
	select {
	case p.inputCh <- in:
		return nil
	default:
		return errors.New("backpressure: buffer full")
	}
}

/*
PROCESS — unchanged logic + inter-arrival tracking
*/
func (p *Processor) process(in SignalInput) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// track inter-arrival time for λ
	if p.lastIngestTime > 0 && in.Time > p.lastIngestTime {
		ia := in.Time - p.lastIngestTime
		p.recentInterArrivals = append(p.recentInterArrivals, ia)
		if len(p.recentInterArrivals) > p.maxArrivalWindow {
			p.recentInterArrivals = p.recentInterArrivals[1:]
		}
	}
	p.lastIngestTime = in.Time

	// build vector
	vec := make([]float64, len(p.schema.Features))
	for i := range vec {
		vec[i] = math.NaN()
	}
	for k, v := range in.Values {
		if idx, ok := p.schema.index[k]; ok {
			vec[idx] = v
		}
	}

	// carry-forward
	if p.lastPoint != nil {
		for i := range vec {
			if math.IsNaN(vec[i]) {
				vec[i] = p.lastPoint.V[i]
			}
		}
	}

	tick := int64(math.Round(in.Time / p.interval))
	t := float64(tick) * p.interval

	if p.lastPoint != nil && tick <= p.lastPoint.Tick {
		return
	}

	point := SignalPoint{Tick: tick, T: t, V: vec, isReal: true}

	if p.lastPoint != nil && tick > p.lastPoint.Tick+1 {
		for tk := p.lastPoint.Tick + 1; tk < tick; tk++ {
			interp := p.interpolate(*p.lastPoint, point, tk)
			interp.isReal = false
			p.insert(interp)
		}
	}

	p.insert(point)
	p.lastPoint = &point
}

/*
INTERPOLATION
*/
func (p *Processor) interpolate(p1, p2 SignalPoint, tick int64) SignalPoint {
	v := make([]float64, len(p1.V))
	t1, t2 := float64(p1.Tick), float64(p2.Tick)
	for i := range v {
		v[i] = p1.V[i] + (p2.V[i]-p1.V[i])*(float64(tick)-t1)/(t2-t1)
	}
	return SignalPoint{Tick: tick, T: float64(tick) * p.interval, V: v}
}

/*
INSERT — extended to capture derivative magnitude for μ estimation
*/
func (p *Processor) insert(pt SignalPoint) {

	if pt.isReal {
		if p.lastEMA == nil {
			p.lastEMA = append([]float64{}, pt.V...)
		} else {
			for i := range pt.V {
				pt.V[i] = p.alpha*pt.V[i] + (1-p.alpha)*p.lastEMA[i]
			}
			p.lastEMA = append([]float64{}, pt.V...)
		}
	}
	raw := append([]float64{}, pt.V...)


	// signal intensity (energy-like metric)
var intensity float64
for _, v := range pt.V {
	intensity += v * v
}
intensity = math.Sqrt(intensity)
p.lastIntensity = intensity
	n := len(pt.V)
	pt.D = make([]float64, n)
	pt.A = make([]float64, n)

	if p.lastPoint != nil && len(p.lastPoint.V) == len(pt.V) {
		var totalDerivMag float64
		for i := 0; i < n; i++ {
			pt.D[i] = (pt.V[i] - p.lastPoint.V[i]) / p.interval
			totalDerivMag += math.Abs(pt.D[i])
			if len(p.lastPoint.D) == len(pt.D) {
				pt.A[i] = (pt.D[i] - p.lastPoint.D[i]) / p.interval
			}
		}
		// record mean |dx/dt| as proxy for service capacity μ
		
	}

	p.stats.Update(pt.V)
	std := p.stats.Std()

	for i := range pt.V {
		if std[i] > 0 {
			if math.Abs(pt.V[i]-p.stats.Mean[i]) > 5*std[i] {
				pt.V[i] = p.stats.Mean[i]
			}
		}
	}

	for i := range pt.V {
		if std[i] > 1e-6 {
	pt.V[i] = (pt.V[i] - p.stats.Mean[i]) / std[i]
} else {
	pt.V[i] = 0
}
	}

	p.matrix.Time = append(p.matrix.Time, pt.T)
	p.matrix.Values = append(p.matrix.Values, pt.V)
	p.matrix.RawValues = append(p.matrix.RawValues, raw)
	p.matrix.Derivatives = append(p.matrix.Derivatives, pt.D)
	p.matrix.Accelerations = append(p.matrix.Accelerations, pt.A)

	if len(p.matrix.Time) > p.window {
		p.matrix.Time = p.matrix.Time[1:]
		p.matrix.Values = p.matrix.Values[1:]
		p.matrix.RawValues = p.matrix.RawValues[1:]
		p.matrix.Derivatives = p.matrix.Derivatives[1:]
		p.matrix.Accelerations = p.matrix.Accelerations[1:]
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GetNodeState — computes M/M/1 physics state variables.
//
// λ = 1 / mean(inter-arrival times)
// μ = 1 + mean(|dx/dt|)    (baseline 1 + processing speed proxy)
// L = ρ/(1-ρ)              when ρ < 1  (M/M/1 stable)
//   = (λ-μ) × window_time  when ρ ≥ 1  (unstable, growing queue)
// W = L / λ                (Little's Law)
// ─────────────────────────────────────────────────────────────────────────────
func (p *Processor) GetNodeState() NodeState {
	p.mu.Lock()
	defer p.mu.Unlock()

	// λ and C_A^2
	lambda := 0.0
	arrivalCV2 := 1.0 // default M/M/1 assumption
	if len(p.recentInterArrivals) > 0 {
		var s float64
		for _, t := range p.recentInterArrivals {
			s += t
		}
		meanIA := s / float64(len(p.recentInterArrivals))
		if meanIA > 1e-6 {
			lambda = 1.0 / meanIA

			// Calculate true variance of inter-arrival times
			var varSum float64
			for _, t := range p.recentInterArrivals {
				diff := t - meanIA
				varSum += diff * diff
			}
			iaVar := varSum / float64(len(p.recentInterArrivals))
			arrivalCV2 = iaVar / (meanIA * meanIA) // C_A^2 formula
		} else {
			lambda = 0
		}
	}

// μ = effective service rate and C_S^2
mu := 1.0
serviceCV2 := 1.0 // default M/M/1 assumption

if len(p.matrix.Derivatives) > 0 {
	var totalVar float64
	var meanSum float64
	var count float64

	for _, dvec := range p.matrix.Derivatives {
		for _, d := range dvec {
			totalVar += d * d
			meanSum += d
			count++
		}
	}

	if count > 0 {
		variance := totalVar / count
		meanD := meanSum / count

		// MATHEMATICAL JUSTIFICATION for ServiceCV2:
		// We define C_S^2 = Var(S) / (E[S])^2, where S is the true service time per request.
		// Since we do not observe S directly, we use the first derivative of the macroscopic metric
		// (e.g., CPU utilization or latency rate of change) as a proxy for the instantaneous workload.
		//
		// ASSUMPTIONS:
		// 1. Ergodicity: The variance of the metric derivative over the time window is proportional 
		//    to the variance of the underlying service process.
		// 2. Stationarity: The mean derivative represents the stable-state drift.
		//
		// LIMITATIONS:
		// - If the metric is a gauge (e.g. Memory) rather than a rate, derivatives may exhibit 
		//   autocorrelation, artificially suppressing the variance estimate.
		// - Sampling rate (interval) acts as a low-pass filter; high-frequency burstiness shorter 
		//   than the interval is lost.
		//
		// ERROR BOUNDS:
		// - The estimate is highly sensitive to denominator noise when the mean drift approaches 0.
		//   We mitigate this by establishing a lower bound `meanD > 1e-6` and capping `serviceCV2 <= 10.0`.
		if math.Abs(meanD) > 1e-6 {
			// Variance calculated above is E[X^2]. We need E[(X - E[X])^2] = E[X^2] - E[X]^2
			trueVar := variance - (meanD * meanD)
			if trueVar < 0 { trueVar = 0 }
			serviceCV2 = trueVar / (meanD * meanD)
		} else {
			// If mean derivative is ~0, fallback to normalized variance + 1
			serviceCV2 = 1.0 + variance
		}

		// Cap extreme burstiness measurements to prevent numerical explosion
		if serviceCV2 > 10.0 { serviceCV2 = 10.0 }

		// inverse relation: high variance → low capacity
		mu = 1.0 / (1.0 + variance)

		// scale to usable range
		mu = 0.5 + mu*2.0
	}
}



// ρ
	load := 0.0
	if mu > 1e-9 {
		if mu > 1e-6 {
	load = lambda / mu
	if load < 0 {
		load = 0
	}
	if load > 5 {
		load = 5 // cap extreme explosion
	}
}
	}

	// L
	queueLen := 0.0
	if load >= 1.0 {
	windowTime := float64(len(p.matrix.Time)) * p.interval
	queueLen = (lambda - mu) * windowTime
	if queueLen < 0 {
		queueLen = 0
	}
} else if load > 0 && load < 1.0 {
	queueLen = load / (1.0 - load)
} else {
	queueLen = 0
}

	// W
	delay := 0.0
	if lambda > 1e-9 {
		delay = queueLen / lambda
	}

	ts := 0.0
	if len(p.matrix.Time) > 0 {
		ts = p.matrix.Time[len(p.matrix.Time)-1]
	}

	return NodeState{
		Load:            load,
		ArrivalRate:     lambda,
		ServiceRate:     mu,
		QueueLength:     queueLen,
		ProcessingDelay: delay,
		Timestamp:       ts,

		ArrivalCV2:      arrivalCV2,
		ServiceCV2:      serviceCV2,

		SignalIntensity: p.lastIntensity,
	}
}

/*
EXPORT JSON
*/
func (p *Processor) ExportJSON() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]interface{}{
		"time":          p.matrix.Time,
		"features":      p.schema.Features,
		"values":        p.matrix.Values,
		"raw_values":    p.matrix.RawValues,
		"derivatives":   p.matrix.Derivatives,
		"accelerations": p.matrix.Accelerations,
		"group":         p.groupID,
	}
	return json.Marshal(out)
}

func (p *Processor) GetMatrix() Matrix {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.matrix
}
