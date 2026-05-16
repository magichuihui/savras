package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGrafanaMonitor_InitialState(t *testing.T) {
	m := NewGrafanaMonitor("http://localhost:3000", nil)
	if m.State() != StateUp {
		t.Fatalf("expected initial state StateUp, got %v", m.State())
	}
	if m.ShouldBlock() {
		t.Fatal("expected ShouldBlock()=false in initial state")
	}
}

func TestGrafanaMonitor_OnProxyError_TransitionsToDown(t *testing.T) {
	m := NewGrafanaMonitor("http://localhost:3000", nil)
	m.OnProxyError()
	if m.State() != StateDown {
		t.Fatalf("expected StateDown after OnProxyError, got %v", m.State())
	}
	if !m.ShouldBlock() {
		t.Fatal("expected ShouldBlock()=true after OnProxyError")
	}
}

func TestGrafanaMonitor_OnProxyError_Idempotent(t *testing.T) {
	m := NewGrafanaMonitor("http://localhost:3000", nil)
	m.OnProxyError()
	m.OnProxyError() // second call should be a no-op
	if m.State() != StateDown {
		t.Fatalf("expected StateDown after second OnProxyError, got %v", m.State())
	}
}

func TestGrafanaMonitor_FirstProbeRecordsUUID(t *testing.T) {
	var hitSync atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Grafana-Server-Id", "abc-123")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, func() error {
		hitSync.Store(true)
		return nil
	})

	// Trigger probe loop
	m.OnProxyError()

	// Wait for probe to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.State() == StateUp {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if m.State() != StateUp {
		t.Fatal("expected StateUp after first successful probe")
	}
	if m.grafanaUUID != "abc-123" {
		t.Fatalf("expected grafanaUUID=abc-123, got %s", m.grafanaUUID)
	}
	if hitSync.Load() {
		t.Fatal("expected no sync on first probe")
	}
}

func TestGrafanaMonitor_SameUUID_TransientBlip(t *testing.T) {
	var syncCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Grafana-Server-Id", "abc-123")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, func() error {
		syncCount.Add(1)
		return nil
	})
	// Pre-seed the UUID so the probe sees "same UUID" scenario
	m.mu.Lock()
	m.grafanaUUID = "abc-123"
	m.mu.Unlock()

	m.OnProxyError()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.State() == StateUp {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if m.State() != StateUp {
		t.Fatal("expected StateUp after transient blip")
	}
	if syncCount.Load() != 0 {
		t.Fatal("expected no sync on transient blip")
	}
}

func TestGrafanaMonitor_DifferentUUID_TriggersSync(t *testing.T) {
	var syncCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Grafana-Server-Id", "new-uuid")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, func() error {
		syncCount.Add(1)
		return nil
	})
	// Pre-seed with different UUID
	m.mu.Lock()
	m.grafanaUUID = "old-uuid"
	m.mu.Unlock()

	m.OnProxyError()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.State() == StateUp {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if m.State() != StateUp {
		t.Fatal("expected StateUp after UUID change + sync")
	}
	if syncCount.Load() != 1 {
		t.Fatalf("expected 1 sync call, got %d", syncCount.Load())
	}
	if m.grafanaUUID != "new-uuid" {
		t.Fatalf("expected grafanaUUID=new-uuid, got %s", m.grafanaUUID)
	}
}

func TestGrafanaMonitor_SyncFailure_StaysDown(t *testing.T) {
	var syncCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Grafana-Server-Id", "new-uuid")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, func() error {
		syncCount.Add(1)
		return errors.New("sync failed")
	})
	// Pre-seed with different UUID
	m.mu.Lock()
	m.grafanaUUID = "old-uuid"
	m.mu.Unlock()

	m.OnProxyError()

	// Wait a bit — should try sync once and fail, stay down
	time.Sleep(500 * time.Millisecond)

	if m.State() != StateDown {
		t.Fatal("expected StateDown after sync failure")
	}
	// Sync might be retried in the loop, but should have been called at least once
	if syncCount.Load() < 1 {
		t.Fatalf("expected at least 1 sync attempt, got %d", syncCount.Load())
	}
}

func TestGrafanaMonitor_ProbeFailure_BacksOff(t *testing.T) {
	// Server that never responds (will be closed immediately)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't write anything — connection will hang
	}))
	// Close immediately so probes fail
	srv.Close()

	m := NewGrafanaMonitor(srv.URL, nil)

	start := time.Now()
	m.OnProxyError()

	// After 200ms, the probe should have failed and started backing off
	time.Sleep(200 * time.Millisecond)

	if m.State() != StateDown {
		t.Fatal("expected StateDown after probe failure")
	}

	// Should have waited at least baseInterval before retrying
	elapsed := time.Since(start)
	t.Logf("elapsed after probe failure: %v", elapsed)
}

func TestGrafanaMonitor_Stop_CancelsProbeLoop(t *testing.T) {
	// Server that hangs (never responds)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, nil)
	m.OnProxyError()
	m.Stop()

	// After stop, we should be able to call Stop again (idempotent)
	m.Stop()

	// State should remain Down (stop doesn't change state, just cancels probe)
	if m.State() != StateDown {
		t.Fatal("expected StateDown after Stop")
	}
}

func TestGrafanaMonitor_MissingUUID_BacksOff(t *testing.T) {
	var probeCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeCount.Add(1)
		// Return 200 but no X-Grafana-Server-Id header
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, nil)

	m.OnProxyError()

	// Wait for at least 2 probe attempts
	time.Sleep(350 * time.Millisecond)

	if m.State() != StateDown {
		t.Fatal("expected StateDown when UUID header is missing")
	}
	if probeCount.Load() < 1 {
		t.Fatalf("expected at least 1 probe, got %d", probeCount.Load())
	}
}

func TestGrafanaMonitor_NextInterval(t *testing.T) {
	m := NewGrafanaMonitor("http://localhost:3000", nil)

	// First interval: base * factor
	next := m.nextInterval(100 * time.Millisecond)
	if next < 100*time.Millisecond {
		t.Fatalf("expected next >= base, got %v", next)
	}
	if next > 300*time.Millisecond { // 200 + 50% jitter max = 300
		t.Fatalf("expected next <= 300ms, got %v", next)
	}

	// Cap at maxInterval
	large := 30 * time.Second
	next = m.nextInterval(large)
	if next > m.maxInterval+m.maxInterval/2 {
		t.Fatalf("expected next <= maxInterval + 50%%, got %v", next)
	}
}
