package observability

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestAccessMetricsRecordsProjectionAndBoundedDenialLabels(t *testing.T) {
	metrics := NewAccessMetrics()
	metrics.ObserveAccess("getMyCapabilities", 200, "success", 12*time.Millisecond)
	metrics.ObserveAccess("getMyCapabilities", 503, "authorization_unavailable", 18*time.Millisecond)
	metrics.ObserveAccess("listMyTenants", 403, "user_access_denied", 2*time.Millisecond)
	metrics.ObserveAccess("getMyCapabilities", 418, "attacker-controlled", time.Millisecond)

	snapshot := metrics.Snapshot()
	if snapshot.ProjectionRequests["200"] != 1 || snapshot.ProjectionRequests["503"] != 1 || snapshot.ProjectionRequests["500"] != 1 {
		t.Fatalf("unexpected projection counters: %#v", snapshot.ProjectionRequests)
	}
	if snapshot.ProjectionDurationCount != 3 || math.Abs(snapshot.ProjectionDurationSeconds-0.031) > 0.0000001 {
		t.Fatalf("unexpected duration aggregate: %#v", snapshot)
	}
	if snapshot.AuthorizationDenials["user_access_denied"] != 1 || len(snapshot.AuthorizationDenials) != 1 {
		t.Fatalf("unexpected denial counters: %#v", snapshot.AuthorizationDenials)
	}
	if _, exists := snapshot.AuthorizationDenials["attacker-controlled"]; exists {
		t.Fatal("unbounded metric label was retained")
	}
}

func TestAccessMetricsIsConcurrencySafe(t *testing.T) {
	metrics := NewAccessMetrics()
	var workers sync.WaitGroup
	for range 100 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			metrics.ObserveAccess("getMyCapabilities", 200, "success", time.Millisecond)
		}()
	}
	workers.Wait()
	if got := metrics.Snapshot().ProjectionRequests["200"]; got != 100 {
		t.Fatalf("expected 100 projection requests, got %d", got)
	}
}
