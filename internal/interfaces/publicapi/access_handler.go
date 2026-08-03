package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	authorizationapplication "github.com/36Dge/yijie-api/internal/modules/authorization/application"
	authorizationdomain "github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	identitydomain "github.com/36Dge/yijie-api/internal/modules/identity/domain"
	tenancydomain "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/36Dge/yijie-api/internal/platform/authn"
	"github.com/google/uuid"
)

const (
	AccessOperationListMyTenants     = "listMyTenants"
	AccessOperationGetMyCapabilities = "getMyCapabilities"

	AccessReasonSuccess               = "success"
	AccessReasonUnauthorized          = "unauthorized"
	AccessReasonUserDenied            = "user_access_denied"
	AccessReasonInvalidTenantContext  = "invalid_tenant_context"
	AccessReasonTenantDenied          = "tenant_access_denied"
	AccessReasonDependencyUnavailable = "authorization_unavailable"
	AccessReasonInternalError         = "internal_error"
)

type AccessTokenVerifier interface {
	Verify(ctx context.Context, encoded string) (authn.Identity, error)
}

type PrincipalResolver interface {
	ResolvePrincipal(ctx context.Context, issuer, subject string) (identitydomain.Principal, error)
}

type TenantDiscovery interface {
	ListActive(ctx context.Context, userID uuid.UUID) ([]tenancydomain.Membership, error)
}

type CapabilityProjector interface {
	Project(ctx context.Context, userID, tenantID uuid.UUID) (authorizationdomain.Projection, error)
}

type AccessMetrics interface {
	ObserveAccess(operation string, status int, reason string, duration time.Duration)
}

type AccessHandlerOption func(*AccessHandler)

func WithAccessClock(clock func() time.Time) AccessHandlerOption {
	return func(handler *AccessHandler) {
		if clock != nil {
			handler.clock = clock
		}
	}
}

type AccessHandler struct {
	verifier   AccessTokenVerifier
	principals PrincipalResolver
	tenants    TenantDiscovery
	projector  CapabilityProjector
	metrics    AccessMetrics
	clock      func() time.Time
}

func NewAccessHandler(
	verifier AccessTokenVerifier,
	principals PrincipalResolver,
	tenants TenantDiscovery,
	projector CapabilityProjector,
	metrics AccessMetrics,
	options ...AccessHandlerOption,
) *AccessHandler {
	handler := &AccessHandler{
		verifier:   verifier,
		principals: principals,
		tenants:    tenants,
		projector:  projector,
		metrics:    metrics,
		clock:      time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(handler)
		}
	}
	return handler
}

func (handler *AccessHandler) ListMyTenants(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	status, reason := http.StatusInternalServerError, AccessReasonInternalError
	defer func() { handler.observe(AccessOperationListMyTenants, status, reason, time.Since(started)) }()

	principal, failure := handler.authenticate(r)
	if failure != nil {
		status, reason = failure.status, failure.reason
		writeAccessFailure(w, *failure)
		return
	}
	if handler == nil || handler.tenants == nil {
		writeAccessFailure(w, internalFailure())
		return
	}
	memberships, err := handler.tenants.ListActive(r.Context(), principal.UserID)
	if err != nil {
		failure = mapTenancyFailure(err)
		status, reason = failure.status, failure.reason
		writeAccessFailure(w, *failure)
		return
	}
	tenants := make([]TenantSelection, len(memberships))
	for index, membership := range memberships {
		if !membership.Valid() {
			writeAccessFailure(w, internalFailure())
			return
		}
		tenants[index] = TenantSelection{TenantId: membership.TenantID, DisplayName: membership.DisplayName}
	}
	status, reason = http.StatusOK, AccessReasonSuccess
	writeAccessJSON(w, status, TenantSelectionList{Tenants: tenants})
}

func (handler *AccessHandler) GetMyCapabilities(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	status, reason := http.StatusInternalServerError, AccessReasonInternalError
	defer func() { handler.observe(AccessOperationGetMyCapabilities, status, reason, time.Since(started)) }()

	principal, failure := handler.authenticate(r)
	if failure != nil {
		status, reason = failure.status, failure.reason
		writeAccessFailure(w, *failure)
		return
	}
	tenantID, err := parseTenantHeader(r.Header.Values("X-Yijie-Tenant-ID"))
	if err != nil {
		mappedFailure := invalidTenantFailure()
		status, reason = mappedFailure.status, mappedFailure.reason
		writeAccessFailure(w, mappedFailure)
		return
	}
	if handler == nil || handler.projector == nil {
		writeAccessFailure(w, internalFailure())
		return
	}
	projection, err := handler.projector.Project(r.Context(), principal.UserID, tenantID)
	if err != nil {
		mappedFailure := mapProjectionFailure(err)
		status, reason = mappedFailure.status, mappedFailure.reason
		writeAccessFailure(w, mappedFailure)
		return
	}
	if !handler.validProjection(projection, tenantID) {
		writeAccessFailure(w, internalFailure())
		return
	}
	capabilities := make([]CapabilityKey, len(projection.Permissions))
	for index, permission := range projection.Permissions {
		capabilities[index] = string(permission)
	}
	status, reason = http.StatusOK, AccessReasonSuccess
	writeAccessJSON(w, status, CapabilityProjection{
		SchemaVersion:         CapabilityProjectionSchemaVersionN1,
		TenantId:              projection.TenantID,
		AuthorizationRevision: projection.AuthorizationRevision,
		ExpiresAt:             projection.ExpiresAt,
		Capabilities:          capabilities,
	})
}

func (handler *AccessHandler) authenticate(r *http.Request) (identitydomain.Principal, *accessFailure) {
	if handler == nil || handler.verifier == nil || handler.principals == nil || r == nil {
		failure := internalFailure()
		return identitydomain.Principal{}, &failure
	}
	encoded, err := bearerToken(r.Header.Values("Authorization"))
	if err != nil {
		failure := unauthorizedFailure()
		return identitydomain.Principal{}, &failure
	}
	identity, err := handler.verifier.Verify(r.Context(), encoded)
	if errors.Is(err, authn.ErrKeyUnavailable) {
		failure := unavailableFailure()
		return identitydomain.Principal{}, &failure
	}
	if err != nil {
		failure := unauthorizedFailure()
		return identitydomain.Principal{}, &failure
	}
	principal, err := handler.principals.ResolvePrincipal(r.Context(), identity.Issuer, identity.Subject)
	switch {
	case errors.Is(err, identitydomain.ErrIdentityNotFound), errors.Is(err, identitydomain.ErrUserSuspended):
		failure := userDeniedFailure()
		return identitydomain.Principal{}, &failure
	case errors.Is(err, identitydomain.ErrIdentityUnavailable):
		failure := unavailableFailure()
		return identitydomain.Principal{}, &failure
	case err != nil, principal.UserID == uuid.Nil:
		failure := internalFailure()
		return identitydomain.Principal{}, &failure
	default:
		return principal, nil
	}
}

func bearerToken(values []string) (string, error) {
	if len(values) != 1 {
		return "", authn.ErrUnauthorized
	}
	return authn.BearerToken(values[0])
}

func (handler *AccessHandler) validProjection(projection authorizationdomain.Projection, tenantID uuid.UUID) bool {
	if handler == nil || handler.clock == nil || projection.TenantID != tenantID ||
		projection.AuthorizationRevision < 1 || projection.AuthorizationRevision > tenancydomain.MaxAuthorizationRevision ||
		len(projection.Permissions) > authorizationapplication.MaxProjectionCapabilities {
		return false
	}
	now := handler.clock().UTC()
	if !projection.ExpiresAt.After(now) || projection.ExpiresAt.After(now.Add(authorizationapplication.MaxProjectionTTL)) {
		return false
	}
	for index, permission := range projection.Permissions {
		if !permission.Known() || (index > 0 && projection.Permissions[index-1] >= permission) {
			return false
		}
	}
	return true
}

func (handler *AccessHandler) observe(operation string, status int, reason string, duration time.Duration) {
	if handler != nil && handler.metrics != nil {
		handler.metrics.ObserveAccess(operation, status, reason, duration)
	}
}

func parseTenantHeader(values []string) (uuid.UUID, error) {
	if len(values) != 1 || len(values[0]) != 36 || strings.TrimSpace(values[0]) != values[0] {
		return uuid.Nil, errors.New("tenant header is missing or invalid")
	}
	parsed, err := uuid.Parse(values[0])
	if err != nil || !strings.EqualFold(values[0], parsed.String()) {
		return uuid.Nil, errors.New("tenant header is missing or invalid")
	}
	return parsed, nil
}

type accessFailure struct {
	status     int
	code       string
	message    string
	reason     string
	challenge  bool
	retryAfter bool
}

func unauthorizedFailure() accessFailure {
	return accessFailure{status: http.StatusUnauthorized, code: "unauthorized", message: "valid bearer authentication is required", reason: AccessReasonUnauthorized, challenge: true}
}

func userDeniedFailure() accessFailure {
	return accessFailure{status: http.StatusForbidden, code: "user_access_denied", message: "user access is suspended or denied", reason: AccessReasonUserDenied}
}

func invalidTenantFailure() accessFailure {
	return accessFailure{status: http.StatusBadRequest, code: "invalid_tenant_context", message: "tenant selection is missing or invalid", reason: AccessReasonInvalidTenantContext}
}

func tenantDeniedFailure() accessFailure {
	return accessFailure{status: http.StatusForbidden, code: "tenant_access_denied", message: "tenant membership is suspended or denied", reason: AccessReasonTenantDenied}
}

func internalFailure() accessFailure {
	return accessFailure{status: http.StatusInternalServerError, code: "internal_error", message: "access operation failed", reason: AccessReasonInternalError}
}

func unavailableFailure() accessFailure {
	return accessFailure{status: http.StatusServiceUnavailable, code: "authorization_unavailable", message: "authorization dependencies are unavailable", reason: AccessReasonDependencyUnavailable, retryAfter: true}
}

func mapTenancyFailure(err error) *accessFailure {
	failure := internalFailure()
	if errors.Is(err, tenancydomain.ErrTenancyUnavailable) {
		failure = unavailableFailure()
	}
	return &failure
}

func mapProjectionFailure(err error) accessFailure {
	switch {
	case errors.Is(err, authorizationdomain.ErrDenied):
		return tenantDeniedFailure()
	case errors.Is(err, authorizationdomain.ErrAuthorizationUnavailable):
		return unavailableFailure()
	default:
		return internalFailure()
	}
}

func writeAccessFailure(w http.ResponseWriter, failure accessFailure) {
	if failure.challenge {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if failure.retryAfter {
		w.Header().Set("Retry-After", "1")
	}
	writeAccessJSON(w, failure.status, ErrorResponse{Code: failure.code, Message: failure.message})
}

func writeAccessJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
