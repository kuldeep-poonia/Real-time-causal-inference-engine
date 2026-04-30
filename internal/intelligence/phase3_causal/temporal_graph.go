package phase3_causal

import (
	"math"
	"sort"
	"time"
)


type TimeConfig struct {
	BucketSize    time.Duration
	MaxAllowedGap time.Duration
}

/*
BUILD TEMPORAL GRAPH
*/
func BuildTemporalGraph(events []Event, cfg TimeConfig) *TemporalGraph {
	graph := &TemporalGraph{
		Nodes: make(map[string]*TemporalSeries),
		Edges: []*TemporalEdge{},
	}

	grouped := groupAndBucketEvents(events, cfg)

	for nodeID, timeMap := range grouped {
		series := buildTemporalSeries(timeMap)
		graph.Nodes[nodeID] = series
	}

	graph.Edges = buildTemporalEdges(graph.Nodes, cfg)

	return graph
}

/*
GROUP + BUCKET EVENTS
*/
func groupAndBucketEvents(events []Event, cfg TimeConfig) map[string]map[time.Time][]Event {
	result := make(map[string]map[time.Time][]Event)

	for _, e := range events {
		bucket := bucketTime(e.Timestamp, cfg.BucketSize)

		if _, ok := result[e.NodeID]; !ok {
			result[e.NodeID] = make(map[time.Time][]Event)
		}

		result[e.NodeID][bucket] = append(result[e.NodeID][bucket], e)
	}

	return result
}

/*
CENTER BASED BUCKETING
*/
func bucketTime(t time.Time, bucketSize time.Duration) time.Time {
	if bucketSize <= 0 {
		return t
	}

	ns := t.UnixNano()
	bucketNs := bucketSize.Nanoseconds()

	rounded := ((ns + bucketNs/2) / bucketNs) * bucketNs
	return time.Unix(0, rounded)
}

/*
BUILD SERIES
*/
func buildTemporalSeries(timeMap map[time.Time][]Event) *TemporalSeries {
	points := make([]TemporalPoint, 0, len(timeMap))

	for ts, events := range timeMap {
		merged := mergeEvents(events)

		node := &TemporalNode{
			NodeID: merged.NodeID,
			Time:   ts,

			Value:     merged.Value,
			Noise:     merged.NoiseLevel,
			Intensity: merged.Intensity,
		}

		points = append(points, TemporalPoint{
			Time: ts,
			Node: node,
		})
	}

	sort.Slice(points, func(i, j int) bool {
		return points[i].Time.Before(points[j].Time)
	})

	return &TemporalSeries{
		Points:  points,
		Density: computeDensity(points),
	}
}

/*
MERGE EVENTS (STABLE)
*/
func mergeEvents(events []Event) Event {
	if len(events) == 1 {
		return events[0]
	}

	var weightedSum float64
	var totalWeight float64

	var sumNoise float64
	var sumIntensity float64

	typeScore := make(map[string]float64)

	for _, e := range events {
		// stable bounded weight
		weight := e.Confidence * math.Exp(-e.NoiseLevel)

		weightedSum += e.Value * weight
		totalWeight += weight

		sumNoise += e.NoiseLevel
		sumIntensity += e.Intensity

		// intensity aware type scoring
		score := e.Intensity * e.Confidence
		typeScore[e.Type] += score
	}

	value := 0.0
	if totalWeight > 0 {
		value = weightedSum / totalWeight
	}

	// dominant type (intensity aware)
	dominantType := ""
	maxScore := -1.0
	for t, s := range typeScore {
		if s > maxScore {
			maxScore = s
			dominantType = t
		}
	}

	n := float64(len(events))
	base := events[0]

	return Event{
		NodeID: base.NodeID,

		Value: value,

		NoiseLevel: sumNoise / n,
		Intensity:  sumIntensity / n,

		Type: dominantType,
	}
}

/*
BUILD TEMPORAL EDGES
*/
func buildTemporalEdges(nodes map[string]*TemporalSeries, cfg TimeConfig) []*TemporalEdge {
    edges := []*TemporalEdge{}

    // ==========================
    // INTRA-NODE EDGES
    // ==========================
    for _, series := range nodes {
        points := series.Points

        for i := 0; i < len(points)-1; i++ {

            current := points[i]
            next := points[i+1]

            lag := next.Time.Sub(current.Time)

            discontinuous := false
            if cfg.MaxAllowedGap > 0 && lag > cfg.MaxAllowedGap {
                discontinuous = true
            }

            strength := math.Abs(next.Node.Value - current.Node.Value)

            edge := &TemporalEdge{
                From: *current.Node,
                To:   *next.Node,

                Lag: lag,

                ExistenceProb:  0.9,
                CausalStrength: strength,

                IsFeedbackLoop: false,
                Identifiable:   false,

                Conditions: map[string]float64{},

                Discontinuous: discontinuous,

                Mean:     0.0,
                Variance: (current.Node.Noise + next.Node.Noise) / 2.0,
            }

            edges = append(edges, edge)
        }
    }

    // ==========================
    // CROSS-NODE CAUSAL EDGES
    // ==========================
    for idA, seriesA := range nodes {
        for idB, seriesB := range nodes {

            if idA == idB {
                continue
            }

            pointsA := seriesA.Points
            pointsB := seriesB.Points

            for i := 0; i < len(pointsA); i++ {
                for j := 0; j < len(pointsB); j++ {

                    tA := pointsA[i].Time
                    tB := pointsB[j].Time

                    lag := tB.Sub(tA)

                    if lag <= 0 {
                        continue
                    }

                    if cfg.MaxAllowedGap > 0 && lag > cfg.MaxAllowedGap {
                        continue
                    }

                    strength := math.Abs(pointsB[j].Node.Value - pointsA[i].Node.Value)

                    edge := &TemporalEdge{
                        From: *pointsA[i].Node,
                        To:   *pointsB[j].Node,

                        Lag: lag,

                        ExistenceProb:  0.5 * seriesA.Density * seriesB.Density,
                        CausalStrength: strength,

                        Discontinuous: false,
                        Variance: (pointsA[i].Node.Noise + pointsB[j].Node.Noise) / 2.0,
                    }

                    edges = append(edges, edge)
                }
            }
        }
    }

    return edges
}




/*
DENSITY (IRREGULAR SAFE)
*/
func computeDensity(points []TemporalPoint) float64 {
	if len(points) < 2 {
		return 0
	}

	var totalGap float64
	var gaps []float64

	for i := 0; i < len(points)-1; i++ {
		gap := points[i+1].Time.Sub(points[i].Time).Seconds()
		totalGap += gap
		gaps = append(gaps, gap)
	}

	avgGap := totalGap / float64(len(gaps))

	var variance float64
	for _, g := range gaps {
		diff := g - avgGap
		variance += diff * diff
	}
	variance /= float64(len(gaps))

	if avgGap == 0 {
		return 0
	}

	return (1.0 / avgGap) * math.Exp(-variance)
}