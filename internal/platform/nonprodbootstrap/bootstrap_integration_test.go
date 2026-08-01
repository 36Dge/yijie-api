//go:build integration

package nonprodbootstrap

import (
	"context"
	"errors"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/36Dge/yijie-api/internal/platform/database"
	"github.com/36Dge/yijie-api/internal/platform/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBootstrapIsIdempotentAuditedAndConflictSafe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := bootstrapDatabase(t, ctx)
	defer cleanup()
	runner := New(pool, Environment, testManifest().Issuer)

	owner := testManifest()
	first, err := runner.Run(ctx, owner)
	if err != nil || !first.StateChanged || first.AuthorizationRevision != 2 || len(first.AuthorizationDiff) == 0 {
		t.Fatalf("first bootstrap: result=%#v err=%v", first, err)
	}
	if len(first.AuthorizationDiff) != 13 || first.AuthorizationDiff[0] != "membership.created" ||
		first.AuthorizationDiff[len(first.AuthorizationDiff)-1] != "assignment.created:tenant_owner" {
		t.Fatalf("unexpected initial authorization diff: %#v", first.AuthorizationDiff)
	}
	second, err := runner.Run(ctx, owner)
	if err != nil || second.StateChanged || second.AuthorizationRevision != 2 || len(second.AuthorizationDiff) != 0 {
		t.Fatalf("idempotent bootstrap: result=%#v err=%v", second, err)
	}

	member := owner
	member.Subject = "synthetic-member"
	member.UserID = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	member.Role = RoleTenantMember
	memberResult, err := runner.Run(ctx, member)
	if err != nil || !memberResult.StateChanged || memberResult.AuthorizationRevision != 3 {
		t.Fatalf("member bootstrap: result=%#v err=%v", memberResult, err)
	}
	if expectedDiff := []string{"membership.created", "assignment.created:tenant_member"}; !slices.Equal(memberResult.AuthorizationDiff, expectedDiff) {
		t.Fatalf("expected member authorization diff %#v, got %#v", expectedDiff, memberResult.AuthorizationDiff)
	}

	conflict := owner
	conflict.Subject = "synthetic-conflicting-user"
	conflict.UserID = uuid.MustParse("44444444-4444-4444-8444-444444444444")
	conflict.TenantName = "Synthetic Conflicting Tenant"
	if _, err := runner.Run(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected tenant conflict, got %v", err)
	}

	assertCount(t, ctx, pool, "users", 2)
	assertCount(t, ctx, pool, "user_identities", 2)
	assertCount(t, ctx, pool, "tenants", 1)
	assertCount(t, ctx, pool, "tenant_memberships", 2)
	assertCount(t, ctx, pool, "roles", 2)
	assertCount(t, ctx, pool, "role_permissions", 9)
	assertCount(t, ctx, pool, "membership_roles", 2)
	assertCount(t, ctx, pool, "audit_logs", 4)
	assertTenantRevision(t, ctx, pool, owner.TenantID, 3)

	var successChanged, successUnchanged, failures int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE outcome = 'success' AND metadata->>'state_changed' = 'true'),
			count(*) FILTER (WHERE outcome = 'success' AND metadata->>'state_changed' = 'false'),
			count(*) FILTER (WHERE outcome = 'failure' AND metadata->>'reason' = 'state_conflict')
		FROM audit_logs
		WHERE action = 'authorization.bootstrap'
	`).Scan(&successChanged, &successUnchanged, &failures); err != nil {
		t.Fatalf("query bootstrap audit: %v", err)
	}
	if successChanged != 2 || successUnchanged != 1 || failures != 1 {
		t.Fatalf("unexpected audit outcomes: changed=%d unchanged=%d failures=%d", successChanged, successUnchanged, failures)
	}

	var revisionTwo, revisionThree, emptyDiff int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE metadata->>'authorization_revision' = '2'),
			count(*) FILTER (WHERE metadata->>'authorization_revision' = '3'),
			count(*) FILTER (WHERE metadata->'authorization_diff' = '[]'::jsonb)
		FROM audit_logs
		WHERE action = 'authorization.bootstrap'
		  AND outcome = 'success'
	`).Scan(&revisionTwo, &revisionThree, &emptyDiff); err != nil {
		t.Fatalf("query bootstrap revision audits: %v", err)
	}
	if revisionTwo != 2 || revisionThree != 1 || emptyDiff != 1 {
		t.Fatalf("unexpected revision audits: revision_two=%d revision_three=%d empty_diff=%d", revisionTwo, revisionThree, emptyDiff)
	}

	var auditText string
	if err := pool.QueryRow(ctx, "SELECT string_agg(metadata::text, '') FROM audit_logs").Scan(&auditText); err != nil {
		t.Fatalf("read audit metadata: %v", err)
	}
	if strings.Contains(auditText, owner.Issuer) || strings.Contains(auditText, owner.Subject) || strings.Contains(auditText, member.Subject) {
		t.Fatal("audit metadata contains issuer or subject")
	}
}

func TestBootstrapBuildsTwoUserTwoTenantAuthorizationMatrix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := bootstrapDatabase(t, ctx)
	defer cleanup()

	userA := testManifest()
	userA.TenantName = "Synthetic FEAT-125 Tenant A"
	userB := userA
	userB.Subject = "synthetic-user-b"
	userB.UserID = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	tenantBID := uuid.MustParse("55555555-5555-4555-8555-555555555555")

	matrix := []Manifest{
		userA,
		func() Manifest {
			value := userA
			value.TenantID = tenantBID
			value.TenantName = "Synthetic FEAT-125 Tenant B"
			value.Role = RoleTenantMember
			return value
		}(),
		func() Manifest {
			value := userB
			value.Role = RoleTenantMember
			return value
		}(),
		func() Manifest {
			value := userB
			value.TenantID = tenantBID
			value.TenantName = "Synthetic FEAT-125 Tenant B"
			value.Role = RoleTenantOwner
			return value
		}(),
	}
	runner := New(pool, Environment, userA.Issuer)
	for index, manifest := range matrix {
		result, err := runner.Run(ctx, manifest)
		if err != nil || !result.StateChanged || len(result.AuthorizationDiff) == 0 {
			t.Fatalf("bootstrap matrix entry %d: result=%#v err=%v", index, result, err)
		}
	}

	type expectedAssignment struct {
		role            string
		permissionCount int
	}
	expected := map[string]expectedAssignment{
		userA.UserID.String() + ":" + userA.TenantID.String(): {role: RoleTenantOwner, permissionCount: 7},
		userA.UserID.String() + ":" + tenantBID.String():      {role: RoleTenantMember, permissionCount: 2},
		userB.UserID.String() + ":" + userA.TenantID.String(): {role: RoleTenantMember, permissionCount: 2},
		userB.UserID.String() + ":" + tenantBID.String():      {role: RoleTenantOwner, permissionCount: 7},
	}
	rows, err := pool.Query(ctx, `
		SELECT assignment.user_id, assignment.tenant_id, role.role_key, count(role_permission.permission_key)
		FROM membership_roles AS assignment
		JOIN roles AS role
		  ON role.tenant_id = assignment.tenant_id
		 AND role.id = assignment.role_id
		JOIN role_permissions AS role_permission
		  ON role_permission.tenant_id = role.tenant_id
		 AND role_permission.role_id = role.id
		GROUP BY assignment.user_id, assignment.tenant_id, role.role_key
		ORDER BY assignment.user_id, assignment.tenant_id
	`)
	if err != nil {
		t.Fatalf("query bootstrap authorization matrix: %v", err)
	}
	defer rows.Close()
	actual := make(map[string]expectedAssignment, len(expected))
	for rows.Next() {
		var userID, tenantID uuid.UUID
		var role string
		var permissionCount int
		if err := rows.Scan(&userID, &tenantID, &role, &permissionCount); err != nil {
			t.Fatalf("scan bootstrap authorization matrix: %v", err)
		}
		actual[userID.String()+":"+tenantID.String()] = expectedAssignment{role: role, permissionCount: permissionCount}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate bootstrap authorization matrix: %v", err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("expected %d matrix entries, got %d: %#v", len(expected), len(actual), actual)
	}
	for scope, expectedValue := range expected {
		if actual[scope] != expectedValue {
			t.Fatalf("scope %s: expected %#v, got %#v", scope, expectedValue, actual[scope])
		}
	}
	assertTenantRevision(t, ctx, pool, userA.TenantID, 3)
	assertTenantRevision(t, ctx, pool, tenantBID, 3)

	for index, manifest := range matrix {
		result, err := runner.Run(ctx, manifest)
		if err != nil || result.StateChanged || result.AuthorizationRevision != 3 || len(result.AuthorizationDiff) != 0 {
			t.Fatalf("idempotent matrix entry %d: result=%#v err=%v", index, result, err)
		}
	}
	assertTenantRevision(t, ctx, pool, userA.TenantID, 3)
	assertTenantRevision(t, ctx, pool, tenantBID, 3)
	assertCount(t, ctx, pool, "audit_logs", 8)
}

func TestBootstrapRollsBackWhenSuccessAuditFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := bootstrapDatabase(t, ctx)
	defer cleanup()
	installBootstrapAuditRejector(t, ctx, pool, "NEW.outcome = 'success'")

	manifest := testManifest()
	result, err := New(pool, Environment, manifest.Issuer).Run(ctx, manifest)
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("expected primary audit failure with recorded failure audit: result=%#v err=%v", result, err)
	}
	assertCount(t, ctx, pool, "users", 0)
	assertCount(t, ctx, pool, "tenants", 0)
	assertCount(t, ctx, pool, "audit_logs", 1)
	var reason string
	if err := pool.QueryRow(ctx, `
		SELECT metadata->>'reason'
		FROM audit_logs
		WHERE action = 'authorization.bootstrap' AND outcome = 'failure'
	`).Scan(&reason); err != nil {
		t.Fatalf("query failure audit after rollback: %v", err)
	}
	if reason != "audit_unavailable" {
		t.Fatalf("expected audit_unavailable reason, got %q", reason)
	}
}

func TestBootstrapReportsWhenFailureAuditCannotBeWritten(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := bootstrapDatabase(t, ctx)
	defer cleanup()
	installBootstrapAuditRejector(t, ctx, pool, "true")

	manifest := testManifest()
	result, err := New(pool, Environment, manifest.Issuer).Run(ctx, manifest)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, ErrAuditUnavailable) {
		t.Fatalf("expected stable primary and failure-audit classifications: result=%#v err=%v", result, err)
	}
	assertCount(t, ctx, pool, "users", 0)
	assertCount(t, ctx, pool, "tenants", 0)
	assertCount(t, ctx, pool, "audit_logs", 0)
}

func TestBootstrapReconcilesCommittedTransactionWhenCommitAcknowledgementIsLost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := bootstrapDatabase(t, ctx)
	defer cleanup()

	manifest := testManifest()
	runner := New(pool, Environment, manifest.Issuer)
	runner.commit = func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return errors.New("injected lost commit acknowledgement")
	}
	result, err := runner.Run(ctx, manifest)
	if err != nil || !result.StateChanged || result.AuthorizationRevision != 2 {
		t.Fatalf("expected reconciled committed result: result=%#v err=%v", result, err)
	}
	assertCount(t, ctx, pool, "users", 1)
	assertCount(t, ctx, pool, "audit_logs", 1)
	assertAuditOutcomeCount(t, ctx, pool, result.RequestID, "success", 1)
	assertAuditOutcomeCount(t, ctx, pool, result.RequestID, "failure", 0)
}

func TestBootstrapRecordsFailureOnlyForDefiniteCommitRollback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := bootstrapDatabase(t, ctx)
	defer cleanup()

	manifest := testManifest()
	runner := New(pool, Environment, manifest.Issuer)
	runner.commit = func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.Rollback(ctx); err != nil {
			return err
		}
		return pgx.ErrTxCommitRollback
	}
	result, err := runner.Run(ctx, manifest)
	if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("expected definite rollback classification: result=%#v err=%v", result, err)
	}
	assertCount(t, ctx, pool, "users", 0)
	assertAuditOutcomeCount(t, ctx, pool, result.RequestID, "success", 0)
	assertAuditOutcomeCount(t, ctx, pool, result.RequestID, "failure", 1)
}

func TestBootstrapDoesNotWriteContradictoryAuditForUnknownCommitOutcome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, cleanup := bootstrapDatabase(t, ctx)
	defer cleanup()

	manifest := testManifest()
	runner := New(pool, Environment, manifest.Issuer)
	runner.commit = func(context.Context, pgx.Tx) error {
		return errors.New("injected ambiguous commit failure")
	}
	result, err := runner.Run(ctx, manifest)
	if !errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected outcome-unknown classification: result=%#v err=%v", result, err)
	}
	assertCount(t, ctx, pool, "users", 0)
	assertAuditOutcomeCount(t, ctx, pool, result.RequestID, "success", 0)
	assertAuditOutcomeCount(t, ctx, pool, result.RequestID, "failure", 0)
}

func installBootstrapAuditRejector(t *testing.T, ctx context.Context, pool *pgxpool.Pool, condition string) {
	t.Helper()
	if condition != "NEW.outcome = 'success'" && condition != "true" {
		t.Fatalf("unsupported test trigger condition %q", condition)
	}
	statement := `
		CREATE FUNCTION reject_bootstrap_audit_for_test()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $function$
		BEGIN
			IF NEW.action = 'authorization.bootstrap' AND ` + condition + ` THEN
				RAISE EXCEPTION 'synthetic bootstrap audit rejected by test';
			END IF;
			RETURN NEW;
		END;
		$function$;
		CREATE TRIGGER reject_bootstrap_audit_for_test
		BEFORE INSERT ON audit_logs
		FOR EACH ROW EXECUTE FUNCTION reject_bootstrap_audit_for_test()
	`
	if _, err := pool.Exec(ctx, statement); err != nil {
		t.Fatalf("install bootstrap audit rejector: %v", err)
	}
}

func assertTenantRevision(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, expected int64) {
	t.Helper()
	var actual int64
	if err := pool.QueryRow(ctx, "SELECT authorization_revision FROM tenants WHERE id = $1", tenantID).Scan(&actual); err != nil {
		t.Fatalf("read tenant authorization revision: %v", err)
	}
	if actual != expected {
		t.Fatalf("expected tenant authorization revision %d, got %d", expected, actual)
	}
}

func assertCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string, expected int) {
	t.Helper()
	var actual int
	query := "SELECT count(*) FROM " + pgx.Identifier{table}.Sanitize()
	if err := pool.QueryRow(ctx, query).Scan(&actual); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if actual != expected {
		t.Fatalf("expected %d %s rows, got %d", expected, table, actual)
	}
}

func assertAuditOutcomeCount(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	requestID string,
	outcome string,
	expected int,
) {
	t.Helper()
	var actual int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_logs WHERE request_id = $1 AND outcome = $2
	`, requestID, outcome).Scan(&actual); err != nil {
		t.Fatalf("count %s audit rows: %v", outcome, err)
	}
	if actual != expected {
		t.Fatalf("expected %d %s audit rows, got %d", expected, outcome, actual)
	}
}

func bootstrapDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	t.Helper()
	baseDSN := os.Getenv("YIJIE_API_TEST_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("YIJIE_API_TEST_POSTGRES_DSN is not set")
	}
	admin, err := database.Open(ctx, baseDSN, 0, 5)
	if err != nil {
		t.Fatalf("open bootstrap integration database: %v", err)
	}
	schema := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("create bootstrap schema: %v", err)
	}
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		admin.Close()
		t.Fatalf("parse bootstrap DSN: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	if err := migrations.Run(ctx, parsed.String(), "up"); err != nil {
		admin.Close()
		t.Fatalf("migrate bootstrap schema: %v", err)
	}
	pool, err := database.Open(ctx, parsed.String(), 0, 5)
	if err != nil {
		admin.Close()
		t.Fatalf("open migrated bootstrap schema: %v", err)
	}
	return pool, func() {
		pool.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanupCtx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		admin.Close()
	}
}
