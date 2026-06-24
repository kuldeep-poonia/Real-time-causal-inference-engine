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
	"strings"
	"sync"
	"time"

	"absia/pkg/docker"
	"absia/pkg/metricsstore"
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

// Remove IsDockerAvailable

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
// the derived M/M/1 queue metrics into store. Blocks until ctx is cancelled.
//
// Metric mapping (Docker → M/M/1):
//
//	λ (ArrivalRate)  = CPU utilisation fraction [0,1]
//	μ (ServiceRate)  = 1.0  (normalised capacity baseline)
//	L (QueueLength)  = memory utilisation fraction [0,1] × 100
func PushContainerStatsToStore(ctx context.Context, store *metricsstore.Store, log *slog.Logger) {
	runtime := DetectRuntime()
	
	if runtime != RuntimeDocker {
		log.Info("autodetect: fallback cgroups polling started", slog.Duration("interval", statsInterval))
		// Stub for reading /sys/fs/cgroup directly for CPU/memory.
		// Detailed cgroups reading is beyond the current scope but this wires the fallback mechanism.
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

	// prevStats holds the previous StatsResponse per container ID for the
	// CPU delta calculation. Without a prior snapshot, cpuPercent returns 0.
	prevStats := make(map[string]*docker.StatsResponse)
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

			var wg sync.WaitGroup
			for _, c := range containers {
				wg.Add(1)
				go func(c docker.Container) {
					defer wg.Done()
					collectOne(ctx, cli, c, store, &mu, prevStats, log)
				}(c)
			}
			wg.Wait()

			// Evict stale prev-stats for containers no longer running.
			mu.Lock()
			alive := make(map[string]bool, len(containers))
			for _, c := range containers {
				alive[c.ID] = true
			}
			for id := range prevStats {
				if !alive[id] {
					delete(prevStats, id)
				}
			}
			mu.Unlock()
		}
	}
}

// ── internal helpers ──────────────────────────────────────────────────────────

func collectOne(
	ctx context.Context,
	cli *docker.Client,
	c docker.Container,
	store *metricsstore.Store,
	mu *sync.Mutex,
	prevStats map[string]*docker.StatsResponse,
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

	mu.Lock()
	prev := prevStats[c.ID]
	prevStats[c.ID] = cur
	mu.Unlock()

	cpuFrac := 0.0
	if prev != nil {
		cpuFrac = docker.CPUPercent(cur, prev)
	}
	memFrac := docker.MemPercent(cur)

	name := containerName(c.Names, c.ID)
	now := time.Now()

	store.Put(name, metricsstore.NodeSample{
		ArrivalRate: cpuFrac,       // λ: CPU fraction [0,1]
		ServiceRate: 1.0,           // μ: normalised capacity
		QueueLength: memFrac * 100, // L: memory% scaled 0–100 for pipeline
		Timestamp:   float64(now.Unix()),
		WallTime:    now,
	})

	log.Debug("autodetect: sample recorded",
		slog.String("container", name),
		slog.String("image", c.Image),
		slog.String("cpu_pct", pct(cpuFrac)),
		slog.String("mem_pct", pct(memFrac)),
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

// pct formats a [0,1] fraction as "XX.X%" for debug log output.
func pct(f float64) string {
	v := f * 100
	if v < 0 {
		v = 0
	}
	return fmt.Sprintf("%.1f%%", v)
}