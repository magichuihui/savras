package proxy

import (
	"context"
	"math/rand"
	"net/http"
	"sync"
	"time"

	slog "log/slog"
)

// GrafanaState represents the lifecycle state of the Grafana backend.
type GrafanaState int

const (
	// StateUp — Grafana is reachable and serving traffic normally.
	StateUp GrafanaState = iota
	// StateDown — Grafana is unreachable or in a restart cycle.
	// Traffic is blocked with 503 until probe confirms recovery.
	StateDown
)

// GrafanaMonitor tracks Grafana's lifecycle using two signals:
//  1. Reverse proxy errors (ECONNREFUSED / 502) — instant crash detection
//  2. Periodic health probes with exponential backoff — recovery detection
//
// When Grafana restarts (detected via X-Grafana-Server-Id change), the
// monitor triggers a blocking sync to rebuild teams and permissions before
// allowing traffic through.
type GrafanaMonitor struct {
	mu          sync.RWMutex
	state       GrafanaState
	grafanaUUID string   // last known X-Grafana-Server-Id
	grafanaAddr string   // base URL for health probes
	syncNowFn   func() error

	probeCancel context.CancelFunc

	// Backoff parameters
	baseInterval time.Duration
	maxInterval  time.Duration
	factor       float64
}

// NewGrafanaMonitor creates a monitor that probes grafanaAddr for health
// and calls syncNowFn when a Grafana restart (UUID change) is detected.
// syncNowFn should be a blocking sync call (e.g. SyncWorker.SyncNow()).
func NewGrafanaMonitor(grafanaAddr string, syncNowFn func() error) *GrafanaMonitor {
	return &GrafanaMonitor{
		state:        StateUp,
		grafanaAddr:  grafanaAddr,
		syncNowFn:    syncNowFn,
		baseInterval: 100 * time.Millisecond,
		maxInterval:  10 * time.Second,
		factor:       2.0,
	}
}

// Start launches the background probe loop. The monitor starts in StateUp
// and only begins probing when OnProxyError() is called or a probe is
// explicitly triggered.
func (m *GrafanaMonitor) Start(ctx context.Context) {
	// Not needed for the reactive probe loop — probes are triggered by
	// OnProxyError(). This method exists for lifecycle management.
}

// Stop cancels any active probe loop.
func (m *GrafanaMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.probeCancel != nil {
		m.probeCancel()
		m.probeCancel = nil
	}
}

// ShouldBlock returns true when Grafana is in StateDown and traffic should
// be rejected with 503.
func (m *GrafanaMonitor) ShouldBlock() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state == StateDown
}

// State returns the current Grafana lifecycle state.
func (m *GrafanaMonitor) State() GrafanaState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// OnProxyError is called by the reverse proxy's ErrorHandler when a request
// to Grafana fails (connection refused, timeout, 502, etc.). It transitions
// to StateDown and starts the exponential-backoff probe loop.
func (m *GrafanaMonitor) OnProxyError() {
	m.mu.Lock()
	if m.state == StateDown {
		// Already in recovery, no need to start another probe loop.
		m.mu.Unlock()
		return
	}
	m.state = StateDown
	m.mu.Unlock()

	slog.Warn("grafana: detected unreachable backend, entering recovery mode")
	go m.probeLoop()
}

// probeLoop runs an exponential-backoff health probe until Grafana responds
// with a valid 200 and the UUID (X-Grafana-Server-Id) is stable. On UUID
// change, it triggers a blocking sync before returning to StateUp.
func (m *GrafanaMonitor) probeLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.probeCancel = cancel
	m.mu.Unlock()
	defer cancel()

	client := &http.Client{Timeout: 3 * time.Second}
	interval := m.baseInterval

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		resp, err := client.Get(m.grafanaAddr + "/api/health")
		if err != nil {
			slog.Warn("grafana: probe failed, backing off",
				"error", err, "next_attempt", interval)
			interval = m.nextInterval(interval)
			time.Sleep(interval)
			continue
		}

		// Read UUID header
		uuid := resp.Header.Get("X-Grafana-Server-Id")
		resp.Body.Close()

		if uuid == "" {
			// Grafana responded but without a server ID — not fully initialized.
			slog.Warn("grafana: probe OK but missing X-Grafana-Server-Id, backing off")
			interval = m.nextInterval(interval)
			time.Sleep(interval)
			continue
		}

		// Compare UUID with last known value
		m.mu.Lock()
		oldUUID := m.grafanaUUID

		if oldUUID == "" {
			// First successful probe — just record the UUID.
			m.grafanaUUID = uuid
			m.state = StateUp
			slog.Info("grafana: first successful probe, recording server id",
				"uuid", uuid)
			m.mu.Unlock()
			return
		}

		if oldUUID == uuid {
			// Same UUID — transient blip, Grafana didn't restart.
			m.state = StateUp
			slog.Info("grafana: recovered (transient), resuming traffic")
			m.mu.Unlock()
			return
		}

		// UUID changed — Grafana restarted.  LOCK is HELD.
		// Do NOT update grafanaUUID yet — wait for sync to succeed so that
		// a failed sync will be re-detected on the next probe attempt.
		m.mu.Unlock()

		slog.Warn("grafana: server id changed, triggering sync",
			"old_uuid", oldUUID, "new_uuid", uuid)

		if m.syncNowFn != nil {
			if err := m.syncNowFn(); err != nil {
				slog.Error("grafana: sync after restart failed, retrying",
					"error", err)
				interval = m.nextInterval(interval)
				time.Sleep(interval)
				continue
			}
			slog.Info("grafana: sync after restart completed successfully")
		}

		// Sync succeeded (or no sync function configured) — record UUID
		// and return to normal.
		m.mu.Lock()
		m.grafanaUUID = uuid
		m.state = StateUp
		m.mu.Unlock()
		slog.Info("grafana: fully recovered, resuming traffic")
		return
	}
}

// nextInterval computes the next backoff interval with jitter.
// Formula: min(current * factor, maxInterval) + random(0, 50% of interval)
func (m *GrafanaMonitor) nextInterval(current time.Duration) time.Duration {
	next := time.Duration(float64(current) * m.factor)
	if next > m.maxInterval {
		next = m.maxInterval
	}
	// Add jitter: up to 50% of the interval
	jitter := time.Duration(rand.Int63n(int64(next) / 2))
	return next + jitter
}
