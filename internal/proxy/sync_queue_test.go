package proxy

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncQueue_CoalescesTriggers(t *testing.T) {
	var count atomic.Int32
	q := NewSyncQueue(func() {
		count.Add(1)
	})

	// Multiple rapid triggers should coalesce into one
	q.Trigger()
	q.Trigger()
	q.Trigger()

	// Give the goroutine time to process
	time.Sleep(100 * time.Millisecond)

	if count.Load() != 1 {
		t.Fatalf("expected 1 sync after coalescing, got %d", count.Load())
	}
}

func TestSyncQueue_SequentialTriggers(t *testing.T) {
	var count atomic.Int32
	q := NewSyncQueue(func() {
		count.Add(1)
	})

	// First batch
	q.Trigger()
	time.Sleep(50 * time.Millisecond)

	// Second batch after first was processed
	q.Trigger()
	time.Sleep(50 * time.Millisecond)

	if count.Load() != 2 {
		t.Fatalf("expected 2 syncs for sequential triggers, got %d", count.Load())
	}
}

func TestSyncQueue_NoopWhenGrafanaUp(t *testing.T) {
	var called atomic.Bool
	q := NewSyncQueue(func() { called.Store(true) })
	q.Trigger()
	time.Sleep(50 * time.Millisecond)
	if !called.Load() {
		t.Fatal("expected sync to fire immediately when no monitor")
	}
}

func TestSyncQueue_TriggerNonBlocking(t *testing.T) {
	q := NewSyncQueue(func() {
		time.Sleep(100 * time.Millisecond)
	})

	// First trigger starts processing (blocks in syncFn)
	q.Trigger()
	time.Sleep(10 * time.Millisecond)

	// Second trigger should not block (channel is full, dropped)
	q.Trigger()

	// Should complete without deadlock
}
