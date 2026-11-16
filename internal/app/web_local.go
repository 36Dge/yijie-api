package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"

	wire "github.com/36Dge/yijie-api/internal/contracts/builtinweb"
	authapp "github.com/36Dge/yijie-api/internal/modules/authorization/application"
	auth "github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	authpg "github.com/36Dge/yijie-api/internal/modules/authorization/infrastructure/postgres"
	tenantapp "github.com/36Dge/yijie-api/internal/modules/tenancy/application"
	tenantpg "github.com/36Dge/yijie-api/internal/modules/tenancy/infrastructure/postgres"
	web "github.com/36Dge/yijie-api/internal/modules/webretrieval/domain"
	webhttp "github.com/36Dge/yijie-api/internal/modules/webretrieval/interfaces/http"
	"github.com/36Dge/yijie-api/internal/platform/ownedcredential"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func NewLocalBuiltinWeb(ctx context.Context, c WebConfig, pool *pgxpool.Pool) (*webhttp.Handler, *http.Server, ownedcredential.Credential, error) {
	if os.Getenv("YIJIE_ENV") != "local" || os.Getenv("YIJIE_LOCAL_PROFILE") != "demo_fast" {
		return nil, nil, ownedcredential.Credential{}, errors.New("local web profile required")
	}
	credential, err := ownedcredential.Read(os.Getenv(wire.LocalCredentialEnvironment))
	if err != nil {
		return nil, nil, credential, err
	}
	user, tenant := uuid.MustParse(wire.LocalOwnerID), uuid.MustParse(wire.LocalTenantID)
	authorizationRepo := authpg.New(pool)
	if err = authorizationRepo.EnsureLocalDemo(ctx, user, tenant); err != nil {
		return nil, nil, credential, err
	}
	tenancy, permissions := tenantapp.New(tenantpg.New(pool)), authapp.New(authorizationRepo)
	identity := func(requireWeb bool) webhttp.ProductIdentity {
		return func(r *http.Request) (web.Scope, error) {
			if r.Header.Get("Origin") != "" || r.Header.Get(wire.LocalEpochHeader) != credential.RunEpoch || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+credential.Token)) != 1 {
				return web.Scope{}, web.Expired
			}
			if header := r.Header.Get("X-Yijie-Tenant-ID"); header != "" && header != tenant.String() {
				return web.Scope{}, web.Denied
			}
			scope, e := tenancy.VerifyActive(r.Context(), user, tenant)
			if e != nil {
				return web.Scope{}, web.Denied
			}
			if requireWeb && permissions.Check(r.Context(), scope, auth.WebRetrieve) != nil {
				return web.Scope{}, web.Denied
			}
			return web.Scope{User: user, Tenant: tenant, Revision: scope.AuthorizationRevision}, nil
		}
	}
	handler, internal, err := NewBuiltinWeb(c, pool, identity(true), identity(false))
	return handler, internal, credential, err
}

// The local entry uses the existing service configuration, but cannot change
// the public listener address or enable a public fixed-identity fallback.
func LoadLocalWebConfig() (WebConfig, error) {
	if os.Getenv("YIJIE_ENV") != "local" || os.Getenv("YIJIE_LOCAL_PROFILE") != "demo_fast" {
		return WebConfig{}, errors.New("local web profile required")
	}
	return loadWebConfig()
}
