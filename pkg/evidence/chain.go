package evidence

import (
	"fmt"
	"time"

	phase5 "absia/internal/intelligence/phase5_insight"
	"absia/pkg/metricsstore"
)

// EvidenceLink represents a single step in the causal chain.
type EvidenceLink struct {
	From      string        `json:"from"`
	To        string        `json:"to"`
	Timestamp time.Time     `json:"at"`
	Delay     time.Duration `json:"delay_seconds"`
	Signal    string        `json:"signal"`
	Magnitude float64       `json:"magnitude"`
	Evidence  string        `json:"evidence"`
}

// CausalChain represents the complete chronological spread of a failure.
type CausalChain struct {
	RootCause string         `json:"root_cause"`
	Links     []EvidenceLink `json:"links"`
	Timeline  []string       `json:"timeline"`
}

// BuildCausalChain traces the SCM graph from the root cause to the target node
// using Breadth-First Search (BFS) and correlates it with temporal metrics data.
func BuildCausalChain(graph *phase5.CausalGraph, rootCause string, target string, store *metricsstore.Store) CausalChain {
	chain := CausalChain{
		RootCause: rootCause,
		Links:     make([]EvidenceLink, 0),
		Timeline:  make([]string, 0),
	}

	if graph == nil || store == nil {
		return chain
	}

	// BFS to find the shortest path from rootCause to target
	path := findPathBFS(graph, rootCause, target)
	if len(path) < 2 {
		return chain
	}

	// Iterate through the path to build evidence links
	for i := 0; i < len(path)-1; i++ {
		fromNode := path[i]
		toNode := path[i+1]

		fromSample, okFrom := store.GetLatestSample(fromNode)
		toSample, okTo := store.GetLatestSample(toNode)

		if !okFrom || !okTo {
			continue
		}

		// Calculate delay
		fromTime := time.UnixMilli(int64(fromSample.Timestamp))
		toTime := time.UnixMilli(int64(toSample.Timestamp))
		
		// Ensure delay is not negative (causality means effect follows cause)
		delay := toTime.Sub(fromTime)
		if delay < 0 {
			delay = 0 
			toTime = fromTime // Clamp for timeline consistency
		}

		// Determine the dominant signal 
		signal := toSample.DominantSignal
		if signal == "" {
			signal = "load"
		}

		// Magnitude is the load/pressure at that time
		magnitude := toSample.ComputePressure
		if magnitude == 0 {
			magnitude = toSample.ArrivalRate
		}

		evidenceDesc := fmt.Sprintf("%s anomaly detected %.1f seconds after %s spike", 
			toNode, delay.Seconds(), fromNode)

		link := EvidenceLink{
			From:      fromNode,
			To:        toNode,
			Timestamp: toTime,
			Delay:     delay,
			Signal:    signal,
			Magnitude: magnitude,
			Evidence:  evidenceDesc,
		}
		
		chain.Links = append(chain.Links, link)
		
		timelineEntry := fmt.Sprintf("%s - %s", toTime.Format("15:04:05"), evidenceDesc)
		chain.Timeline = append(chain.Timeline, timelineEntry)
	}

	return chain
}

// findPathBFS implements a standard Breadth-First Search to find the shortest path 
// in the directed causal graph.
func findPathBFS(graph *phase5.CausalGraph, start string, end string) []string {
	if start == end {
		return []string{start}
	}

	queue := [][]string{{start}}
	visited := make(map[string]bool)
	visited[start] = true

	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]

		nodeID := path[len(path)-1]

		// Find children of nodeID
		for _, edge := range graph.Edges {
			if edge.From.ID == nodeID {
				child := edge.To.ID
				if child == end {
					return append(path, child)
				}
				if !visited[child] {
					visited[child] = true
					newPath := make([]string, len(path))
					copy(newPath, path)
					newPath = append(newPath, child)
					queue = append(queue, newPath)
				}
			}
		}
	}
	
	return []string{start, end}
}
