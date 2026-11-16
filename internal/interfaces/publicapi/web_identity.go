package publicapi

import (
	"errors"
	"net/http"

	auth "github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	tenancy "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	web "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	webhttp "github.com/36Dge/yijie-api/internal/modules/webretrieval/interfaces/http"
)

func (handler *AccessHandler) WebIdentity(tenants ActiveTenantVerifier, permissions TaskPermissionChecker) webhttp.ProductIdentity {
	return handler.webIdentity(tenants, permissions, true)
}
func (handler *AccessHandler) WebRevocationIdentity(tenants ActiveTenantVerifier) webhttp.ProductIdentity {
	return handler.webIdentity(tenants, nil, false)
}
func (handler *AccessHandler) webIdentity(tenants ActiveTenantVerifier, permissions TaskPermissionChecker, requirePermission bool) webhttp.ProductIdentity {
	return func(r *http.Request) (web.Scope, error) {
		principal, failure := handler.authenticate(r)
		if failure != nil {
			if failure.status == http.StatusUnauthorized {
				return web.Scope{}, web.Expired
			}
			if failure.status == http.StatusForbidden {
				return web.Scope{}, web.Denied
			}
			return web.Scope{}, web.Unavailable
		}
		id, err := parseTenantHeader(r.Header.Values("X-Yijie-Tenant-ID"))
		if err != nil {
			return web.Scope{}, web.Invalid
		}
		scope, err := tenants.VerifyActive(r.Context(), principal.UserID, id)
		if errors.Is(err, tenancy.ErrTenantAccessDenied) {
			return web.Scope{}, web.Denied
		}
		if err != nil {
			return web.Scope{}, web.Unavailable
		}
		if !scope.Valid() || scope.UserID != principal.UserID || scope.TenantID != id {
			return web.Scope{}, web.Denied
		}
		if requirePermission {
			if err = permissions.Check(r.Context(), scope, auth.WebRetrieve); err != nil {
				if errors.Is(err, auth.ErrDenied) {
					return web.Scope{}, web.Denied
				}
				return web.Scope{}, web.Unavailable
			}
		}
		return web.Scope{User: scope.UserID, Tenant: scope.TenantID, Revision: scope.AuthorizationRevision}, nil
	}
}
