package postgres

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestE2EProjectionIsContentFree(t *testing.T) {
	projection := E2EProjection{
		SchemaVersion: 1,
		Status:        "passed",
		Profile:       FEAT126S10Profile,
		Tasks:         ControlPlaneCounts{Count: 1, Enums: []string{"draft"}, CanonicalHash: strings.Repeat("a", 64)},
		Audit:         ControlPlaneCounts{Count: 1, Enums: []string{"success"}, CanonicalHash: strings.Repeat("b", 64)},
		Idempotency:   ControlPlaneCounts{Count: 1, Enums: []string{"bound"}, CanonicalHash: strings.Repeat("c", 64)},
		DenylistHits:  0,
		CanonicalHash: strings.Repeat("d", 64),
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"title", "path", "secret", "bearer", "dsn", "payload", "task body"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("projection contains forbidden material %q: %s", forbidden, encoded)
		}
	}
}

func TestVerifyE2ERejectsNonLocalProfile(t *testing.T) {
	if _, err := VerifyE2E(nil, nil, "production"); err == nil {
		t.Fatal("expected non-local profile to fail closed")
	}
}
