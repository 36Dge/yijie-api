package observability

import (
	"math"
	"strconv"
	"sync"
	"time"
)

const (
	PermissionProjectionRequestsMetric = "permission_projection_requests_total"
	PermissionProjectionDurationMetric = "permission_projection_duration_seconds"
	AuthorizationDenialsMetric         = "authorization_denials_total"
)

var allowedAccessReasons = map[string]struct{}{
	"success":                   {},
	"unauthorized":              {},
	"user_access_denied":        {},
	"invalid_tenant_context":    {},
	"tenant_access_denied":      {},
	"authorization_unavailable": {},
	"internal_error":            {},
}

type AccessMetrics struct {
	mu                        sync.Mutex
	projectionRequests        map[string]uint64
	projectionDurationCount   uint64
	projectionDurationSeconds float64
	authorizationDenials      map[string]uint64
}

type AccessMetricsSnapshot struct {
	ProjectionRequests        map[string]uint64
	ProjectionDurationCount   uint64
	ProjectionDurationSeconds float64
	AuthorizationDenials      map[string]uint64
}

func NewAccessMetrics() *AccessMetrics {
	return &AccessMetrics{
		projectionRequests:   make(map[string]uint64),
		authorizationDenials: make(map[string]uint64),
	}
}

func (metrics *AccessMetrics) ObserveAccess(operation string, status int, reason string, duration time.Duration) {
	if metrics == nil || (operation != "listMyTenants" && operation != "getMyCapabilities") {
		return
	}
	status = boundedStatus(status)
	reason = boundedReason(reason)
	if duration < 0 {
		duration = 0
	}
	durationSeconds := duration.Seconds()
	if math.IsNaN(durationSeconds) || math.IsInf(durationSeconds, 0) {
		durationSeconds = 0
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if operation == "getMyCapabilities" {
		metrics.projectionRequests[strconv.Itoa(status)]++
		metrics.projectionDurationCount++
		metrics.projectionDurationSeconds += durationSeconds
	}
	if status == 400 || status == 401 || status == 403 {
		metrics.authorizationDenials[reason]++
	}
}

func (metrics *AccessMetrics) Snapshot() AccessMetricsSnapshot {
	if metrics == nil {
		return AccessMetricsSnapshot{ProjectionRequests: map[string]uint64{}, AuthorizationDenials: map[string]uint64{}}
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	return AccessMetricsSnapshot{
		ProjectionRequests:        cloneCounters(metrics.projectionRequests),
		ProjectionDurationCount:   metrics.projectionDurationCount,
		ProjectionDurationSeconds: metrics.projectionDurationSeconds,
		AuthorizationDenials:      cloneCounters(metrics.authorizationDenials),
	}
}

func boundedStatus(status int) int {
	switch status {
	case 200, 400, 401, 403, 500, 503:
		return status
	default:
		return 500
	}
}

func boundedReason(reason string) string {
	if _, ok := allowedAccessReasons[reason]; ok {
		return reason
	}
	return "internal_error"
}

func cloneCounters(source map[string]uint64) map[string]uint64 {
	result := make(map[string]uint64, len(source))
	for label, value := range source {
		result[label] = value
	}
	return result
}
