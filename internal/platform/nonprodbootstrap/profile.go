package nonprodbootstrap

import (
	"errors"
	"net/url"
	"strings"
)

const (
	LocalLabProfile  = "feat-125-local-lab"
	LocalLabIssuer   = "https://localhost:8443/realms/yijie-local"
	localLabHost     = "127.0.0.1:5432"
	localLabDatabase = "/yijie_api_feat125_local"
	localLabUserA    = "12500000-0000-4000-8000-000000000001"
	localLabUserB    = "12500000-0000-4000-8000-000000000002"
	localLabTenantA  = "12500000-0000-4000-8000-100000000001"
	localLabTenantB  = "12500000-0000-4000-8000-100000000002"
	localLabActor    = "nonprod:feat-125-g3-local"
)

var ErrInvalidExecutionProfile = errors.New("nonproduction bootstrap execution profile is invalid")

// ValidateExecutionProfile applies additional fail-closed boundaries to explicit
// bootstrap profiles. An empty profile preserves the generic nonproduction
// bootstrap behavior; any named profile must be recognized exactly.
func ValidateExecutionProfile(profile, environment, issuer, dsn string) error {
	if profile == "" {
		return nil
	}
	if profile != LocalLabProfile || environment != Environment || issuer != LocalLabIssuer || !validLocalLabDSN(dsn) {
		return ErrInvalidExecutionProfile
	}
	return nil
}

// ValidateExecutionManifest binds the explicit local-lab profile to the four
// reviewed synthetic identity/tenant/role tuples. The generic profile remains
// governed by Manifest.Validate alone.
func ValidateExecutionManifest(profile string, manifest Manifest) error {
	if profile == "" {
		return nil
	}
	if profile != LocalLabProfile || manifest.Validate(Environment, LocalLabIssuer) != nil ||
		manifest.Subject != manifest.UserID.String() || manifest.ActorID != localLabActor {
		return ErrInvalidExecutionProfile
	}

	expectedTenantName := ""
	expectedRole := ""
	switch manifest.UserID.String() + ":" + manifest.TenantID.String() {
	case localLabUserA + ":" + localLabTenantA:
		expectedTenantName = "Synthetic FEAT-125 Tenant A"
		expectedRole = RoleTenantOwner
	case localLabUserA + ":" + localLabTenantB:
		expectedTenantName = "Synthetic FEAT-125 Tenant B"
		expectedRole = RoleTenantMember
	case localLabUserB + ":" + localLabTenantA:
		expectedTenantName = "Synthetic FEAT-125 Tenant A"
		expectedRole = RoleTenantMember
	case localLabUserB + ":" + localLabTenantB:
		expectedTenantName = "Synthetic FEAT-125 Tenant B"
		expectedRole = RoleTenantOwner
	default:
		return ErrInvalidExecutionProfile
	}
	if manifest.TenantName != expectedTenantName || manifest.Role != expectedRole {
		return ErrInvalidExecutionProfile
	}
	return nil
}

func validLocalLabDSN(dsn string) bool {
	if dsn == "" || dsn != strings.TrimSpace(dsn) || strings.Contains(dsn, "#") {
		return false
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" || parsed.Opaque != "" || parsed.Host != localLabHost ||
		parsed.Path != localLabDatabase || parsed.RawPath != "" || parsed.RawQuery != "sslmode=disable" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.User == nil ||
		parsed.User.Username() == "" {
		return false
	}
	password, hasPassword := parsed.User.Password()
	return hasPassword && password != ""
}
