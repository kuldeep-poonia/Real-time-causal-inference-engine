package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

const (
	// socketPath is the canonical Docker daemon socket location.
	socketPath = "/var/run/docker.sock"

	// apiVersion is the minimum Docker Engine API version ABSIA requires.
	// v1.41 ships with Docker 20.10 (2021) — safe to assume in any modern env.
	apiVersion = "v1.41"

	// dialTimeout is the maximum time to wait for the unix socket connection.
	dialTimeout = 3 * time.Second

	// requestTimeout is the maximum time for a single Docker API call.
	// Stats calls can be slow on loaded hosts; 10s is conservative.
	requestTimeout = 10 * time.Second
)

// Client is a minimal Docker Engine API client backed by the unix socket.
// It is safe to use from multiple goroutines.
type Client struct {
	hc *http.Client
}

// NewClient returns a Client connected to the Docker unix socket.
// The underlying http.Transport reuses connections across calls.
func NewClient() *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := &net.Dialer{Timeout: dialTimeout}
			return d.DialContext(ctx, "unix", socketPath)
		},
		// Disable HTTP/2 — not supported over unix sockets in all daemon versions.
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   0, // no TLS on unix socket
		ResponseHeaderTimeout: requestTimeout,
	}
	return &Client{hc: &http.Client{Transport: tr, Timeout: requestTimeout}}
}

// IsAvailable returns true when the Docker unix socket exists and accepts
// connections. Safe to call at startup without a full Client.
func IsAvailable() bool {
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ListContainers returns all running containers (All=false).
// Equivalent to `docker ps`.
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	var out []Container
	if err := c.get(ctx, "/containers/json", &out); err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}
	return out, nil
}

// ContainerStats fetches a single stats snapshot for containerID
// (stream=false — one response, no long-poll).
func (c *Client) ContainerStats(ctx context.Context, containerID string) (*StatsResponse, error) {
	var out StatsResponse
	path := fmt.Sprintf("/containers/%s/stats?stream=false&one-shot=true", containerID)
	if err := c.get(ctx, path, &out); err != nil {
		return nil, fmt.Errorf("docker: stats %s: %w", containerID[:min(12, len(containerID))], err)
	}
	return &out, nil
}

// ── internal helpers ──────────────────────────────────────────────────────────

// get issues a GET to the Docker API and JSON-decodes the response into dst.
// The URL scheme is irrelevant (unix socket); we use "http://localhost" as a
// placeholder so net/http is happy with the URL format.
func (c *Client) get(ctx context.Context, path string, dst any) error {
	url := fmt.Sprintf("http://localhost/%s%s", apiVersion, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(dst)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}