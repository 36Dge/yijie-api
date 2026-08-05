package nonprodbootstrap

import (
	"errors"
	"net/url"
	"strings"
)

const (
	LocalLabProfile            = "feat-125-local-lab"
	Feat126S10LocalLabProfile  = "feat-126-s10-local-lab"
	LocalLabIssuer             = "https://localhost:8443/realms/yijie-local"
	localLabHost               = "127.0.0.1:5432"
	localLabDatabase           = "/yijie_api_feat125_local"
	feat126S10LocalLabDatabase = "/yijie_api_feat126_s10"
	localLabUserA              = "12500000-0000-4000-8000-000000000001"
	localLabUserB              = "12500000-0000-4000-8000-000000000002"
	localLabTenantA            = "12500000-0000-4000-8000-100000000001"
	localLabTenantB            = "12500000-0000-4000-8000-100000000002"
	localLabActor              = "nonprod:feat-125-g3-local"
)

var ErrInvalidExecutionProfile = errors.New("nonproduction bootstrap execution profile is invalid")

// ValidateExecutionProfile applies additional fail-closed boundaries to explicit
// bootstrap profiles. An empty profile preserves the generic nonproduction
// bootstrap behavior; any named profile must be recognized exactly.
func ValidateExecutionProfile(profile, environment, issuer, dsn string) error {
	if profile == "" {
		return nil
	}
	if environment != Environment || issuer != LocalLabIssuer {
		return ErrInvalidExecutionProfile
	}
	switch profile {
	case LocalLabProfile:
		if validClosedLocalLabDSN(dsn, localLabDatabase) {
			return nil
		}
	case Feat126S10LocalLabProfile:
		if validClosedLocalLabDSN(dsn, feat126S10LocalLabDatabase) {
			return nil
		}
	}
	return ErrInvalidExecutionProfile
}

// ValidateExecutionManifest binds the explicit local-lab profile to the four
// reviewed synthetic identity/tenant/role tuples. The generic profile remains
// governed by Manifest.Validate alone.
func ValidateExecutionManifest(profile string, manifest Manifest) error {
	if profile == "" {
		return nil
	}
	if (profile != LocalLabProfile && profile != Feat126S10LocalLabProfile) ||
		manifest.Validate(Environment, LocalLabIssuer) != nil ||
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

// ValidateExecutionMatrix accepts only the reviewed FEAT-126 S10 manifest
// order. It is intentionally unavailable to the generic and FEAT-125 profiles.
func ValidateExecutionMatrix(profile string, manifests []Manifest) error {
	if profile != Feat126S10LocalLabProfile || len(manifests) != 4 {
		return ErrInvalidExecutionProfile
	}
	expected := [4]string{
		localLabUserA + ":" + localLabTenantA,
		localLabUserA + ":" + localLabTenantB,
		localLabUserB + ":" + localLabTenantA,
		localLabUserB + ":" + localLabTenantB,
	}
	for index, manifest := range manifests {
		if ValidateExecutionManifest(profile, manifest) != nil ||
			manifest.UserID.String()+":"+manifest.TenantID.String() != expected[index] {
			return ErrInvalidExecutionProfile
		}
	}
	return nil
}

func validLocalLabDSN(dsn string) bool {
	return validClosedLocalLabDSN(dsn, localLabDatabase)
}

func validClosedLocalLabDSN(dsn, database string) bool {
	if dsn == "" || dsn != strings.TrimSpace(dsn) || strings.Contains(dsn, "#") {
		return false
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" || parsed.Opaque != "" || parsed.Host != localLabHost ||
		parsed.Path != database || parsed.RawPath != "" || parsed.RawQuery != "sslmode=disable" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.User == nil ||
		parsed.User.Username() == "" {
		return false
	}
	password, hasPassword := parsed.User.Password()
	return hasPassword && password != ""
}
