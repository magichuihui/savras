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
	// StateDown — Grafana is unreachable.
	// Traffic is blocked with 503 until probe confirms recovery.
	StateDown
)

// GrafanaMonitor tracks Grafana's lifecycle using two signals:
//  1. Reverse proxy errors (ECONNREFUSED / 502) — instant crash detection
//  2. Periodic health probes with exponential backoff — recovery detection
type GrafanaMonitor struct {
	mu          sync.RWMutex
	state       GrafanaState
	grafanaAddr string // base URL for health probes

	probeCancel context.CancelFunc

	// onRecovery is called once when Grafana transitions from StateDown
	// back to StateUp, before traffic resumes.
	onRecovery func()

	// Backoff parameters
	baseInterval time.Duration
	maxInterval  time.Duration
	factor       float64
}

// NewGrafanaMonitor creates a monitor that probes grafanaAddr for health.
// When Grafana recovers from a down state, onRecovery is called (if non-nil)
// before traffic resumes. Use this to trigger a sync on restart.
func NewGrafanaMonitor(grafanaAddr string, onRecovery func()) *GrafanaMonitor {
	return &GrafanaMonitor{
		state:        StateUp,
		grafanaAddr:  grafanaAddr,
		onRecovery:   onRecovery,
		baseInterval: 1 * time.Second,
		maxInterval:  10 * time.Second,
		factor:       2.0,
	}
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
		m.mu.Unlock()
		return
	}
	m.state = StateDown
	m.mu.Unlock()

	slog.Warn("grafana: detected unreachable backend, entering recovery mode")
	go m.probeLoop()
}

// probeLoop runs an exponential-backoff health probe until Grafana responds
// with a successful status code, then transitions back to StateUp.
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
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			slog.Warn("grafana: probe returned non-200, backing off",
				"status", resp.StatusCode, "next_attempt", interval)
			interval = m.nextInterval(interval)
			time.Sleep(interval)
			continue
		}

		// Grafana is reachable — fire recovery callback then resume traffic.
		// Capture onRecovery outside the lock since it may block (e.g. sync).
		m.mu.Lock()
		m.state = StateUp
		recoveryFn := m.onRecovery
		m.mu.Unlock()

		if recoveryFn != nil {
			slog.Info("grafana: backend recovered, running recovery callback")
			recoveryFn()
		}

		slog.Info("grafana: backend reachable, resuming traffic")
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
	jitter := time.Duration(rand.Int63n(int64(next) / 2))
	return next + jitter
}
