package proxy

import (
	"time"
)

// SyncQueue coalesces multiple sync triggers into a single execution.
// When Grafana is down, triggers are queued but not executed immediately.
// Once Grafana becomes reachable, a single sync runs (merging all pending
// triggers). The queue is independent of the recovery callback — the
// recovery path in GrafanaMonitor remains blocking (SyncNow direct call).
//
// Trigger coalescing
//   T1 ─┐
//   T2 ─┤     ┌─────────────┐    ┌──────────────────┐    ┌─────────┐
//   T3 ─┤────▶│ chan (size 1)│───▶│ drain + wait Up │───▶│ syncOne │
//        └───▶│ (coalesce)  │    └──────────────────┘    └─────────┘
type SyncQueue struct {
	trigger chan struct{}
	syncFn  func()
}

// NewSyncQueue creates a queue that coalesces triggers and calls syncFn
// once when Grafana is reachable. syncFn should be a blocking sync call.
func NewSyncQueue(syncFn func()) *SyncQueue {
	q := &SyncQueue{
		trigger: make(chan struct{}, 1),
		syncFn:  syncFn,
	}
	go q.loop()
	return q
}

// Trigger enqueues a sync request. Multiple rapid triggers are coalesced
// into one (buffered channel of size 1). Non-blocking.
func (q *SyncQueue) Trigger() {
	select {
	case q.trigger <- struct{}{}:
	default:
	}
}

func (q *SyncQueue) loop() {
	for range q.trigger {
		// Drain any additional triggers that arrived while we were waiting
		// (non-blocking drain, coalesce all into one).
		for len(q.trigger) > 0 {
			<-q.trigger
		}

		// If Grafana is down, wait until it comes back up before syncing.
		// This avoids wasted sync attempts during downtime.
		if grafanaMonitor != nil {
			for grafanaMonitor.State() == StateDown {
				time.Sleep(time.Second)
			}
		}

		if q.syncFn != nil {
			q.syncFn()
		}
	}
}
