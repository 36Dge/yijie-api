package nonprodbootstrap

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const validLocalLabDSNForTest = "postgres://feat125:local-secret@127.0.0.1:5432/yijie_api_feat125_local?sslmode=disable"

func TestValidateExecutionProfile(t *testing.T) {
	if err := ValidateExecutionProfile(LocalLabProfile, Environment, LocalLabIssuer, validLocalLabDSNForTest); err != nil {
		t.Fatalf("validate local-lab profile: %v", err)
	}
	if err := ValidateExecutionProfile("", Environment, "https://generic.example.invalid/", "postgres://generic.example.invalid/other"); err != nil {
		t.Fatalf("empty profile must preserve generic nonproduction behavior: %v", err)
	}

	tests := []struct {
		name        string
		profile     string
		environment string
		issuer      string
		dsn         string
	}{
		{name: "wrong profile", profile: "feat-125-local", environment: Environment, issuer: LocalLabIssuer, dsn: validLocalLabDSNForTest},
		{name: "production environment", profile: LocalLabProfile, environment: "production", issuer: LocalLabIssuer, dsn: validLocalLabDSNForTest},
		{name: "wrong issuer", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer + "/", dsn: validLocalLabDSNForTest},
		{name: "wrong host", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat125:local-secret@localhost:5432/yijie_api_feat125_local?sslmode=disable"},
		{name: "wrong port", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat125:local-secret@127.0.0.1:5433/yijie_api_feat125_local?sslmode=disable"},
		{name: "missing port", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat125:local-secret@127.0.0.1/yijie_api_feat125_local?sslmode=disable"},
		{name: "wrong database", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat125:local-secret@127.0.0.1:5432/yijie_api?sslmode=disable"},
		{name: "encoded database", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat125:local-secret@127.0.0.1:5432/yijie_api_feat125%5Flocal?sslmode=disable"},
		{name: "missing query", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat125:local-secret@127.0.0.1:5432/yijie_api_feat125_local"},
		{name: "insecure query drift", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat125:local-secret@127.0.0.1:5432/yijie_api_feat125_local?sslmode=allow"},
		{name: "extra query", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: validLocalLabDSNForTest + "&application_name=bootstrap"},
		{name: "duplicate query", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: validLocalLabDSNForTest + "&sslmode=disable"},
		{name: "empty fragment delimiter", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: validLocalLabDSNForTest + "#"},
		{name: "keyword dsn", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "host=127.0.0.1 port=5432 dbname=yijie_api_feat125_local sslmode=disable"},
		{name: "missing username", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://:local-secret@127.0.0.1:5432/yijie_api_feat125_local?sslmode=disable"},
		{name: "missing password", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat125@127.0.0.1:5432/yijie_api_feat125_local?sslmode=disable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateExecutionProfile(test.profile, test.environment, test.issuer, test.dsn)
			if !errors.Is(err, ErrInvalidExecutionProfile) {
				t.Fatalf("expected invalid execution profile, got %v", err)
			}
			if strings.Contains(err.Error(), test.dsn) || strings.Contains(err.Error(), "local-secret") {
				t.Fatalf("error leaked DSN material: %v", err)
			}
		})
	}
}

func TestValidateExecutionManifest(t *testing.T) {
	valid := Manifest{
		SchemaVersion:      SchemaVersion,
		DataClassification: DataClassification,
		Issuer:             LocalLabIssuer,
		Subject:            localLabUserA,
		UserID:             uuid.MustParse(localLabUserA),
		TenantID:           uuid.MustParse(localLabTenantA),
		TenantName:         "Synthetic FEAT-125 Tenant A",
		Role:               RoleTenantOwner,
		ActorID:            localLabActor,
	}
	if err := ValidateExecutionManifest(LocalLabProfile, valid); err != nil {
		t.Fatalf("validate local-lab manifest: %v", err)
	}
	if err := ValidateExecutionManifest("", testManifest()); err != nil {
		t.Fatalf("empty profile must preserve generic manifest behavior: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "wrong issuer", mutate: func(manifest *Manifest) { manifest.Issuer += "/" }},
		{name: "wrong subject", mutate: func(manifest *Manifest) { manifest.Subject = localLabUserB }},
		{name: "unknown user", mutate: func(manifest *Manifest) {
			manifest.Subject = "12500000-0000-4000-8000-000000000003"
			manifest.UserID = uuid.MustParse(manifest.Subject)
		}},
		{name: "unknown tenant", mutate: func(manifest *Manifest) {
			manifest.TenantID = uuid.MustParse("12500000-0000-4000-8000-100000000003")
		}},
		{name: "wrong tenant name", mutate: func(manifest *Manifest) { manifest.TenantName = "Synthetic FEAT-125 Tenant B" }},
		{name: "wrong role", mutate: func(manifest *Manifest) { manifest.Role = RoleTenantMember }},
		{name: "wrong actor", mutate: func(manifest *Manifest) { manifest.ActorID = "nonprod:other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := valid
			test.mutate(&manifest)
			if err := ValidateExecutionManifest(LocalLabProfile, manifest); !errors.Is(err, ErrInvalidExecutionProfile) {
				t.Fatalf("expected fixed matrix rejection, got %v", err)
			}
		})
	}
	if err := ValidateExecutionManifest("unknown", valid); !errors.Is(err, ErrInvalidExecutionProfile) {
		t.Fatalf("unknown explicit profile must be rejected, got %v", err)
	}
}
