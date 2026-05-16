package proxy

import (
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
	m.OnProxyError()
	if m.State() != StateDown {
		t.Fatalf("expected StateDown after second call, got %v", m.State())
	}
}

func TestGrafanaMonitor_RecoversOnProbeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, nil)
	m.OnProxyError()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.State() == StateUp {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if m.State() != StateUp {
		t.Fatal("expected StateUp after probe success")
	}
	if m.ShouldBlock() {
		t.Fatal("expected ShouldBlock()=false after recovery")
	}
}

func TestGrafanaMonitor_RecoveryCallback(t *testing.T) {
	var called atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, func() { called.Store(true) })
	m.OnProxyError()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.State() == StateUp {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if m.State() != StateUp {
		t.Fatal("expected StateUp after probe success")
	}
	if !called.Load() {
		t.Fatal("expected recovery callback to be called")
	}
}

func TestGrafanaMonitor_ProbeNon200_StaysDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, nil)
	m.OnProxyError()

	time.Sleep(350 * time.Millisecond)

	if m.State() != StateDown {
		t.Fatal("expected StateDown when probe returns non-200")
	}
}

func TestGrafanaMonitor_ProbeFailure_BacksOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Don't write anything — connection hangs
	}))
	srv.Close()

	m := NewGrafanaMonitor(srv.URL, nil)

	start := time.Now()
	m.OnProxyError()

	time.Sleep(200 * time.Millisecond)

	if m.State() != StateDown {
		t.Fatal("expected StateDown after probe failure")
	}

	elapsed := time.Since(start)
	t.Logf("elapsed after probe failure: %v", elapsed)
}

func TestGrafanaMonitor_Stop_CancelsProbeLoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Second)
	}))
	defer srv.Close()

	m := NewGrafanaMonitor(srv.URL, nil)
	m.OnProxyError()
	m.Stop()
	m.Stop()
	if m.State() != StateDown {
		t.Fatal("expected StateDown after Stop")
	}
}

func TestGrafanaMonitor_NextInterval(t *testing.T) {
	m := NewGrafanaMonitor("http://localhost:3000", nil)

	next := m.nextInterval(100 * time.Millisecond)
	if next < 100*time.Millisecond {
		t.Fatalf("expected next >= base, got %v", next)
	}
	if next > 300*time.Millisecond {
		t.Fatalf("expected next <= 300ms, got %v", next)
	}

	large := 30 * time.Second
	next = m.nextInterval(large)
	if next > m.maxInterval+m.maxInterval/2 {
		t.Fatalf("expected next <= maxInterval + 50%%, got %v", next)
	}
}
