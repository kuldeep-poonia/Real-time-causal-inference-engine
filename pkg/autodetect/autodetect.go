// Package autodetect provides plug-and-play Docker container discovery and
// metrics collection using only the stdlib Docker HTTP client in pkg/docker.
//
// No external dependencies. Gracefully degrades when the Docker socket is
// absent — callers check IsDockerAvailable() first and skip if false.
package autodetect

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"absia/pkg/docker"
	"absia/pkg/metricsstore"
	"absia/pkg/signals"
)

const (
	// discoveryInterval is how often the container list is re-logged.
	discoveryInterval = 30 * time.Second

	// statsInterval is how often per-container CPU/memory stats are polled
	// and pushed into the metrics store.
	// 8s × 4 samples = 32 seconds to first real pipeline run.
	statsInterval = 8 * time.Second
)

// ContainerInfo is the minimal metadata exposed to callers (e.g. NodesHandler).
type ContainerInfo struct {
	ID    string
	Name  string
	Image string
	State string
}

// DiscoverContainers returns the list of currently running containers.
// Used on-demand by the /nodes HTTP handler to enrich the node inventory.
func DiscoverContainers(ctx context.Context) ([]ContainerInfo, error) {
	cli := docker.NewClient()
	raw, err := cli.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, 0, len(raw))
	for _, c := range raw {
		out = append(out, ContainerInfo{
			ID:    c.ID,
			Name:  containerName(c.Names, c.ID),
			Image: c.Image,
			State: c.State,
		})
	}
	return out, nil
}

// StartContainerDiscovery logs all running containers at startup and then
// re-logs every discoveryInterval. Blocks until ctx is cancelled.
func StartContainerDiscovery(ctx context.Context, log *slog.Logger) {
	runtime := DetectRuntime()
	
	if runtime != RuntimeDocker {
		log.Info("autodetect: container discovery running in fallback mode", slog.String("runtime", string(runtime)))
		// Stub discovery for non-Docker runtimes. In a full implementation this would query containerd/crio sockets.
		return
	}

	cli := docker.NewClient()

	log.Info("autodetect: container discovery started")
	listAndLog(ctx, cli, log)

	ticker := time.NewTicker(discoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("autodetect: container discovery stopped")
			return
		case <-ticker.C:
			listAndLog(ctx, cli, log)
		}
	}
}

// PushContainerStatsToStore polls Docker stats every statsInterval and writes
// the derived metrics into store. Blocks until ctx is cancelled.
func PushContainerStatsToStore(ctx context.Context, store *metricsstore.Store, log *slog.Logger) {
	runtime := DetectRuntime()
	
	if runtime != RuntimeDocker {
		log.Info("autodetect: fallback cgroups polling started", slog.Duration("interval", statsInterval))
		ticker := time.NewTicker(statsInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// no-op: raw cgroups reading implementation goes here
			}
		}
	}

	cli := docker.NewClient()

	baselines := make(map[string]*signals.AdaptiveBaseline)
	var mu sync.Mutex

	log.Info("autodetect: metrics collection started", slog.Duration("interval", statsInterval))

	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("autodetect: metrics collection stopped")
			return

		case <-ticker.C:
			containers, err := cli.ListContainers(ctx)
			if err != nil {
				log.Warn("autodetect: list containers failed", slog.Any("error", err))
				continue
			}

			// Ensure a baseline exists for every currently running container
			mu.Lock()
			for _, c := range containers {
				if _, exists := baselines[c.ID]; !exists {
					baselines[c.ID] = signals.NewAdaptiveBaseline(c.ID)
				}
			}
			mu.Unlock()

			var wg sync.WaitGroup
			for _, c := range containers {
				wg.Add(1)
				go func(c docker.Container) {
					defer wg.Done()
					
					mu.Lock()
					baseline := baselines[c.ID]
					mu.Unlock()
					
					if baseline != nil {
						collectOne(ctx, cli, c, store, baseline, log)
					}
				}(c)
			}
			wg.Wait()

			// Evict stale baselines for containers no longer running.
			mu.Lock()
			alive := make(map[string]bool, len(containers))
			for _, c := range containers {
				alive[c.ID] = true
			}
			for id := range baselines {
				if !alive[id] {
					delete(baselines, id)
				}
			}
			mu.Unlock()
		}
	}
}

// ── internal helpers ──────────────────────────────────────────────────────────

func extractNetworkSnapshot(cur *docker.StatsResponse) signals.NetworkSnapshot {
	var rxBytes, txBytes, rxDropped, txDropped, rxPackets, txPackets uint64
	for _, net := range cur.Networks {
		rxBytes += net.RxBytes
		txBytes += net.TxBytes
		rxDropped += net.RxDropped
		txDropped += net.TxDropped
		rxPackets += net.RxPackets
		txPackets += net.TxPackets
	}
	return signals.NetworkSnapshot{
		RxBytes:   rxBytes,
		TxBytes:   txBytes,
		RxDropped: rxDropped,
		TxDropped: txDropped,
		RxPackets: rxPackets,
		TxPackets: txPackets,
		Timestamp: time.Now(),
	}
}

func collectOne(
	ctx context.Context,
	cli *docker.Client,
	c docker.Container,
	store *metricsstore.Store,
	baseline *signals.AdaptiveBaseline,
	log *slog.Logger,
) {
	cur, err := cli.ContainerStats(ctx, c.ID)
	if err != nil {
		log.Debug("autodetect: stats fetch failed",
			slog.String("id", shortID(c.ID)),
			slog.Any("error", err),
		)
		return
	}

	// Update baseline states and fetch the previous ones for delta calculation
	prevMemory := baseline.UpdateMemoryStats(cur)
	
	curNetSnap := extractNetworkSnapshot(cur)
	prevNetSnap := baseline.UpdateNetwork(curNetSnap)

	var prevCPU *signals.PreviousCPU
	if cur.CPUStats.CPUUsage.TotalUsage > 0 {
		prevCPU = baseline.UpdateCPU(cur.CPUStats.CPUUsage.TotalUsage, cur.CPUStats.SystemCPUUsage)
	}

	// Skip computation on the very first cycle until we have two points for delta math
	if prevCPU == nil || prevMemory == nil || prevNetSnap == nil {
		return
	}

	// 1. Gather component signals
	computeSig := signals.CollectCompute(cur, prevCPU)
	memorySig := signals.CollectMemory(cur, prevMemory, statsInterval.Seconds())
	netSig := signals.CollectNetwork(cur, prevNetSnap, statsInterval.Seconds())

	// 2. Fuse signals into unified causal pipeline inputs
	fused := signals.FuseSignals(computeSig, memorySig, netSig, baseline)

	name := containerName(c.Names, c.ID)
	now := time.Now()

	// 0. Fallback Chain Check (OTLP > Docker)
	if latest, ok := store.GetLatestSample(name); ok {
		if latest.MetricSource == "otel" && time.Since(latest.WallTime) < 15*time.Second {
			// We have fresh, high-quality OTLP data. Skip Docker polling.
			return
		}
		// In a full implementation, this is where we'd check detectServiceMesh and scrape Envoy.
	}

	// 3. Write into the standard pipeline store
	store.Put(name, metricsstore.NodeSample{
		ArrivalRate:     fused.ArrivalRate,
		ServiceRate:     fused.ServiceRate,
		QueueLength:     fused.QueueLength,
		ComputePressure: fused.ComputePressure,
		MemoryPressure:  fused.MemoryPressure,
		NetworkPressure: fused.NetworkPressure,
		DominantSignal:  fused.DominantSignal,
		Timestamp:       float64(now.Unix()),
		WallTime:        now,
		MetricSource:    "docker",
		MetricQuality:   0.6, // Mid-quality fallback
	})

	log.Debug("autodetect: sample recorded",
		slog.String("container", name),
		slog.String("dominant", fused.DominantSignal),
		slog.String("arr", pct(fused.ArrivalRate)),
		slog.String("srv", pct(fused.ServiceRate)),
		slog.String("queue", pct(fused.QueueLength)),
	)
}

func listAndLog(ctx context.Context, cli *docker.Client, log *slog.Logger) {
	cs, err := cli.ListContainers(ctx)
	if err != nil {
		log.Warn("autodetect: discovery failed", slog.Any("error", err))
		return
	}
	if len(cs) == 0 {
		log.Info("autodetect: no running containers found")
		return
	}
	for _, c := range cs {
		log.Info("autodetect: container",
			slog.String("name", containerName(c.Names, c.ID)),
			slog.String("image", c.Image),
			slog.String("state", c.State),
		)
	}
}

func containerName(names []string, id string) string {
	if len(names) > 0 {
		return strings.TrimPrefix(names[0], "/")
	}
	return shortID(id)
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// pct formats a fraction as "XX.X%" for debug log output.
func pct(val float64) string {
	v := val * 100
	if v < 0 {
		v = 0
	}
	return fmt.Sprintf("%.2f%%", val*100)
}

// detectServiceMesh checks if an Envoy or Linkerd sidecar metrics endpoint is available.
func detectServiceMesh(containerIP string) bool {
	if containerIP == "" {
		return false
	}
	// Check standard Envoy admin / Prometheus port
	client := http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s:15000/stats/prometheus", containerIP))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			return true
		}
	}
	return false
}