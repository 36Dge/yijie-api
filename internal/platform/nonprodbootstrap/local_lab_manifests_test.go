package nonprodbootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestTrackedLocalLabManifestMatrix(t *testing.T) {
	const (
		userA   = "12500000-0000-4000-8000-000000000001"
		userB   = "12500000-0000-4000-8000-000000000002"
		tenantA = "12500000-0000-4000-8000-100000000001"
		tenantB = "12500000-0000-4000-8000-100000000002"
	)
	type expectedManifest struct {
		userID     string
		tenantID   string
		tenantName string
		role       string
	}
	expected := map[string]expectedManifest{
		"user-a-tenant-a.json": {userID: userA, tenantID: tenantA, tenantName: "Synthetic FEAT-125 Tenant A", role: RoleTenantOwner},
		"user-a-tenant-b.json": {userID: userA, tenantID: tenantB, tenantName: "Synthetic FEAT-125 Tenant B", role: RoleTenantMember},
		"user-b-tenant-a.json": {userID: userB, tenantID: tenantA, tenantName: "Synthetic FEAT-125 Tenant A", role: RoleTenantMember},
		"user-b-tenant-b.json": {userID: userB, tenantID: tenantB, tenantName: "Synthetic FEAT-125 Tenant B", role: RoleTenantOwner},
	}

	directory := filepath.Join("..", "..", "..", "config", "nonproduction", LocalLabProfile)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read local-lab manifest directory: %v", err)
	}
	if len(entries) != len(expected) {
		t.Fatalf("local-lab manifest set must contain exactly %d files, got %d", len(expected), len(entries))
	}
	seen := make(map[string]bool, len(expected))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected directory in local-lab manifest set: %s", entry.Name())
		}
		want, ok := expected[entry.Name()]
		if !ok {
			t.Fatalf("unexpected local-lab manifest: %s", entry.Name())
		}
		file, err := os.Open(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatalf("open local-lab manifest %s: %v", entry.Name(), err)
		}
		manifest, decodeErr := DecodeManifest(file)
		closeErr := file.Close()
		if decodeErr != nil || closeErr != nil {
			t.Fatalf("decode local-lab manifest %s: decode=%v close=%v", entry.Name(), decodeErr, closeErr)
		}
		if err := manifest.Validate(Environment, LocalLabIssuer); err != nil {
			t.Fatalf("validate local-lab manifest %s: %v", entry.Name(), err)
		}
		if err := ValidateExecutionManifest(LocalLabProfile, manifest); err != nil {
			t.Fatalf("bind local-lab manifest %s to fixed matrix: %v", entry.Name(), err)
		}
		if manifest.Issuer != LocalLabIssuer || manifest.Subject != want.userID ||
			manifest.UserID != uuid.MustParse(want.userID) || manifest.TenantID != uuid.MustParse(want.tenantID) ||
			manifest.TenantName != want.tenantName || manifest.Role != want.role ||
			manifest.ActorID != "nonprod:feat-125-g3-local" {
			t.Fatalf("local-lab manifest %s drifted from the fixed synthetic matrix: %#v", entry.Name(), manifest)
		}
		seen[entry.Name()] = true
	}
	for filename := range expected {
		if !seen[filename] {
			t.Fatalf("missing local-lab manifest: %s", filename)
		}
	}
}
