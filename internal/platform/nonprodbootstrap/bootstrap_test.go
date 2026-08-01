package nonprodbootstrap

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestManifestValidation(t *testing.T) {
	valid := testManifest()
	if err := valid.Validate(Environment, valid.Issuer); err != nil {
		t.Fatalf("validate manifest: %v", err)
	}

	tests := []struct {
		name           string
		environment    string
		expectedIssuer string
		mutate         func(*Manifest)
	}{
		{name: "production environment", environment: "production", expectedIssuer: valid.Issuer},
		{name: "missing expected issuer", environment: Environment},
		{name: "mismatched expected issuer", environment: Environment, expectedIssuer: "https://other-identity.example.invalid/"},
		{name: "wrong schema", environment: Environment, expectedIssuer: valid.Issuer, mutate: func(input *Manifest) { input.SchemaVersion = 2 }},
		{name: "unattested data", environment: Environment, expectedIssuer: valid.Issuer, mutate: func(input *Manifest) { input.DataClassification = "unknown" }},
		{name: "http issuer", environment: Environment, expectedIssuer: "http://identity.example.invalid/", mutate: func(input *Manifest) { input.Issuer = "http://identity.example.invalid/" }},
		{name: "issuer user info", environment: Environment, expectedIssuer: "https://user@identity.example.invalid/", mutate: func(input *Manifest) { input.Issuer = "https://user@identity.example.invalid/" }},
		{name: "subject control", environment: Environment, expectedIssuer: valid.Issuer, mutate: func(input *Manifest) { input.Subject = "synthetic\nsubject" }},
		{name: "nil user", environment: Environment, expectedIssuer: valid.Issuer, mutate: func(input *Manifest) { input.UserID = uuid.Nil }},
		{name: "same user and tenant", environment: Environment, expectedIssuer: valid.Issuer, mutate: func(input *Manifest) { input.TenantID = input.UserID }},
		{name: "non synthetic name", environment: Environment, expectedIssuer: valid.Issuer, mutate: func(input *Manifest) { input.TenantName = "Real tenant" }},
		{name: "unknown role", environment: Environment, expectedIssuer: valid.Issuer, mutate: func(input *Manifest) { input.Role = "admin" }},
		{name: "unscoped actor", environment: Environment, expectedIssuer: valid.Issuer, mutate: func(input *Manifest) { input.ActorID = "operator" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			if test.mutate != nil {
				test.mutate(&input)
			}
			if err := input.Validate(test.environment, test.expectedIssuer); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected invalid input, got %v", err)
			}
		})
	}
}

func TestDecodeManifestIsStrictAndBounded(t *testing.T) {
	input, err := DecodeManifest(strings.NewReader(`{
		"schema_version": 1,
		"data_classification": "synthetic_only",
		"issuer": "https://identity.example.invalid/",
		"subject": "synthetic-owner",
		"user_id": "11111111-1111-4111-8111-111111111111",
		"tenant_id": "22222222-2222-4222-8222-222222222222",
		"tenant_name": "Synthetic FEAT-125 Tenant",
		"role": "tenant_owner",
		"actor_id": "nonprod:test"
	}`))
	if err != nil || input.Role != RoleTenantOwner {
		t.Fatalf("decode manifest: input=%#v err=%v", input, err)
	}
	if _, err := DecodeManifest(strings.NewReader(`{"schema_version":1,"unexpected":true}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected unknown field rejection, got %v", err)
	}
	if _, err := DecodeManifest(strings.NewReader(`{"schema_version":1,"schema_version":1}`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected duplicate field rejection, got %v", err)
	}
	if _, err := DecodeManifest(strings.NewReader(strings.Repeat(" ", maxManifestBytes+1))); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected oversized manifest rejection, got %v", err)
	}
}

func TestNilRunnerFailsClosed(t *testing.T) {
	result, err := (*Runner)(nil).Run(t.Context(), testManifest())
	if !errors.Is(err, ErrUnavailable) || result.RequestID == "" {
		t.Fatalf("expected unavailable result with request ID, got result=%#v err=%v", result, err)
	}
}

func TestRunnerRejectsProductionBeforeStorage(t *testing.T) {
	runner := New(nil, "production", testManifest().Issuer)
	_, err := runner.Run(t.Context(), testManifest())
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("production must be rejected before storage, got %v", err)
	}
}

func TestFailureAuditErrorCompositionPreservesPrimaryClassification(t *testing.T) {
	if err := composeFailure(ErrConflict, nil); !errors.Is(err, ErrConflict) || errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("successful failure audit must return only primary classification: %v", err)
	}
	composed := composeFailure(ErrConflict, errors.New("write failed"))
	if !errors.Is(composed, ErrConflict) || !errors.Is(composed, ErrAuditUnavailable) {
		t.Fatalf("composed failure must preserve both stable classifications: %v", composed)
	}
}

func testManifest() Manifest {
	return Manifest{
		SchemaVersion:      SchemaVersion,
		DataClassification: DataClassification,
		Issuer:             "https://identity.example.invalid/",
		Subject:            "synthetic-owner",
		UserID:             uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		TenantID:           uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		TenantName:         "Synthetic FEAT-125 Tenant",
		Role:               RoleTenantOwner,
		ActorID:            "nonprod:test",
	}
}
