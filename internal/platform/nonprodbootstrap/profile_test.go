package nonprodbootstrap

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

const validLocalLabDSNForTest = "postgres://feat125:local-secret@127.0.0.1:5432/yijie_api_feat125_local?sslmode=disable"
const validFeat126S10LocalLabDSNForTest = "postgres://feat126:local-secret@127.0.0.1:5432/yijie_api_feat126_s10?sslmode=disable"

func TestValidateExecutionProfile(t *testing.T) {
	if err := ValidateExecutionProfile(LocalLabProfile, Environment, LocalLabIssuer, validLocalLabDSNForTest); err != nil {
		t.Fatalf("validate local-lab profile: %v", err)
	}
	if err := ValidateExecutionProfile(Feat126S10LocalLabProfile, Environment, LocalLabIssuer, validFeat126S10LocalLabDSNForTest); err != nil {
		t.Fatalf("validate FEAT-126 S10 local-lab profile: %v", err)
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
		{name: "FEAT-126 profile with FEAT-125 database", profile: Feat126S10LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: validLocalLabDSNForTest},
		{name: "FEAT-125 profile with FEAT-126 database", profile: LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: validFeat126S10LocalLabDSNForTest},
		{name: "FEAT-126 wrong host", profile: Feat126S10LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: "postgres://feat126:local-secret@localhost:5432/yijie_api_feat126_s10?sslmode=disable"},
		{name: "FEAT-126 extra query", profile: Feat126S10LocalLabProfile, environment: Environment, issuer: LocalLabIssuer, dsn: validFeat126S10LocalLabDSNForTest + "&application_name=bootstrap"},
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
	if err := ValidateExecutionManifest(Feat126S10LocalLabProfile, valid); err != nil {
		t.Fatalf("validate FEAT-126 S10 local-lab manifest: %v", err)
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
			if err := ValidateExecutionManifest(Feat126S10LocalLabProfile, manifest); !errors.Is(err, ErrInvalidExecutionProfile) {
				t.Fatalf("expected FEAT-126 fixed matrix rejection, got %v", err)
			}
		})
	}
	if err := ValidateExecutionManifest("unknown", valid); !errors.Is(err, ErrInvalidExecutionProfile) {
		t.Fatalf("unknown explicit profile must be rejected, got %v", err)
	}
}

func TestValidateExecutionMatrixIsFeat126OnlyAndOrdered(t *testing.T) {
	manifests := localLabTestMatrix()
	if err := ValidateExecutionMatrix(Feat126S10LocalLabProfile, manifests); err != nil {
		t.Fatalf("expected reviewed FEAT-126 matrix: %v", err)
	}
	if err := ValidateExecutionMatrix(LocalLabProfile, manifests); !errors.Is(err, ErrInvalidExecutionProfile) {
		t.Fatalf("FEAT-125 profile must not expose batch matrix: %v", err)
	}
	if err := ValidateExecutionMatrix("", manifests); !errors.Is(err, ErrInvalidExecutionProfile) {
		t.Fatalf("generic profile must not expose batch matrix: %v", err)
	}
	if err := ValidateExecutionMatrix(Feat126S10LocalLabProfile, manifests[:3]); !errors.Is(err, ErrInvalidExecutionProfile) {
		t.Fatalf("incomplete matrix must fail: %v", err)
	}
	reordered := append([]Manifest(nil), manifests...)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	if err := ValidateExecutionMatrix(Feat126S10LocalLabProfile, reordered); !errors.Is(err, ErrInvalidExecutionProfile) {
		t.Fatalf("reordered matrix must fail: %v", err)
	}
}

func localLabTestMatrix() []Manifest {
	manifest := testManifest()
	manifest.Issuer = LocalLabIssuer
	manifest.ActorID = localLabActor
	manifest.Subject = localLabUserA
	manifest.UserID = uuid.MustParse(localLabUserA)
	manifest.TenantID = uuid.MustParse(localLabTenantA)
	manifest.TenantName = "Synthetic FEAT-125 Tenant A"
	manifest.Role = RoleTenantOwner

	userATenantB := manifest
	userATenantB.TenantID = uuid.MustParse(localLabTenantB)
	userATenantB.TenantName = "Synthetic FEAT-125 Tenant B"
	userATenantB.Role = RoleTenantMember
	userBTenantA := manifest
	userBTenantA.Subject = localLabUserB
	userBTenantA.UserID = uuid.MustParse(localLabUserB)
	userBTenantA.Role = RoleTenantMember
	userBTenantB := userBTenantA
	userBTenantB.TenantID = uuid.MustParse(localLabTenantB)
	userBTenantB.TenantName = "Synthetic FEAT-125 Tenant B"
	userBTenantB.Role = RoleTenantOwner
	return []Manifest{manifest, userATenantB, userBTenantA, userBTenantB}
}
