package nonprodbootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ExecutionStateEmpty    = "empty"
	ExecutionStateComplete = "complete"
)

var ErrInvalidExecutionState = errors.New("nonproduction bootstrap execution state is invalid")

type ExecutionStateVerification struct {
	State                    string `json:"state"`
	Users                    int    `json:"users"`
	UserIdentities           int    `json:"user_identities"`
	Tenants                  int    `json:"tenants"`
	TenantMemberships        int    `json:"tenant_memberships"`
	Roles                    int    `json:"roles"`
	RolePermissions          int    `json:"role_permissions"`
	MembershipRoles          int    `json:"membership_roles"`
	Tasks                    int    `json:"tasks"`
	TaskCreateIdempotency    int    `json:"task_create_idempotency"`
	BootstrapAudits          int    `json:"bootstrap_audits"`
	ChangedBootstrapAudits   int    `json:"changed_bootstrap_audits"`
	UnchangedBootstrapAudits int    `json:"unchanged_bootstrap_audits"`
}

// VerifyExecutionState validates only the closed FEAT-126 S10 synthetic database
// states. It owns the application-schema queries so Infra never copies business SQL.
func VerifyExecutionState(
	ctx context.Context,
	pool *pgxpool.Pool,
	profile string,
	expectedState string,
) (ExecutionStateVerification, error) {
	result := ExecutionStateVerification{State: expectedState}
	if pool == nil || profile != Feat126S10LocalLabProfile ||
		(expectedState != ExecutionStateEmpty && expectedState != ExecutionStateComplete) {
		return result, ErrInvalidExecutionState
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()

	counts := []struct {
		name   string
		target *int
	}{
		{name: "users", target: &result.Users},
		{name: "user_identities", target: &result.UserIdentities},
		{name: "tenants", target: &result.Tenants},
		{name: "tenant_memberships", target: &result.TenantMemberships},
		{name: "roles", target: &result.Roles},
		{name: "role_permissions", target: &result.RolePermissions},
		{name: "membership_roles", target: &result.MembershipRoles},
		{name: "tasks", target: &result.Tasks},
		{name: "task_create_idempotency", target: &result.TaskCreateIdempotency},
	}
	for _, count := range counts {
		query := "SELECT count(*) FROM " + pgx.Identifier{count.name}.Sanitize()
		if err := tx.QueryRow(ctx, query).Scan(count.target); err != nil {
			return result, ErrUnavailable
		}
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action = 'authorization.bootstrap'").Scan(&result.BootstrapAudits); err != nil {
		return result, ErrUnavailable
	}
	if err := tx.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE (metadata->>'state_changed')::boolean),
			count(*) FILTER (WHERE NOT (metadata->>'state_changed')::boolean)
		FROM audit_logs
		WHERE action = 'authorization.bootstrap'
	`).Scan(&result.ChangedBootstrapAudits, &result.UnchangedBootstrapAudits); err != nil {
		return result, ErrUnavailable
	}

	if expectedState == ExecutionStateEmpty {
		if result != (ExecutionStateVerification{State: ExecutionStateEmpty}) {
			return result, ErrInvalidExecutionState
		}
		if err := tx.Commit(ctx); err != nil {
			return result, ErrUnavailable
		}
		return result, nil
	}

	expected := ExecutionStateVerification{
		State:                    ExecutionStateComplete,
		Users:                    2,
		UserIdentities:           2,
		Tenants:                  2,
		TenantMemberships:        4,
		Roles:                    4,
		RolePermissions:          18,
		MembershipRoles:          4,
		BootstrapAudits:          8,
		ChangedBootstrapAudits:   4,
		UnchangedBootstrapAudits: 4,
	}
	if result != expected {
		return result, ErrInvalidExecutionState
	}
	userA := uuid.MustParse(localLabUserA)
	userB := uuid.MustParse(localLabUserB)
	tenantA := uuid.MustParse(localLabTenantA)
	tenantB := uuid.MustParse(localLabTenantB)

	checks := []struct {
		query string
		args  []any
		want  int
	}{
		{
			query: "SELECT count(*) FROM users WHERE id IN ($1, $2) AND status = 'active'",
			args:  []any{userA, userB},
			want:  2,
		},
		{
			query: `SELECT count(*) FROM user_identities
				WHERE issuer = $1
				  AND ((subject = $2 AND user_id = $3) OR (subject = $4 AND user_id = $5))`,
			args: []any{LocalLabIssuer, localLabUserA, userA, localLabUserB, userB},
			want: 2,
		},
		{
			query: `SELECT count(*) FROM tenants
				WHERE status = 'active' AND authorization_revision = 3
				  AND ((id = $1 AND name = 'Synthetic FEAT-125 Tenant A')
				    OR (id = $2 AND name = 'Synthetic FEAT-125 Tenant B'))`,
			args: []any{tenantA, tenantB},
			want: 2,
		},
		{
			query: `SELECT count(*) FROM tenant_memberships
				WHERE status = 'active' AND tenant_id IN ($1, $2) AND user_id IN ($3, $4)`,
			args: []any{tenantA, tenantB, userA, userB},
			want: 4,
		},
		{
			query: `SELECT count(*) FROM roles
				WHERE tenant_id IN ($1, $2) AND is_system
				  AND ((role_key = 'tenant_owner' AND name = 'Tenant owner')
				    OR (role_key = 'tenant_member' AND name = 'Tenant member'))`,
			args: []any{tenantA, tenantB},
			want: 4,
		},
		{
			query: `SELECT count(*)
				FROM role_permissions AS permission
				JOIN roles AS role ON role.tenant_id = permission.tenant_id AND role.id = permission.role_id
				WHERE role.tenant_id IN ($1, $2)
				  AND ((role.role_key = 'tenant_owner' AND permission.permission_key = ANY($3::text[]))
				    OR (role.role_key = 'tenant_member' AND permission.permission_key = ANY($4::text[])))`,
			args: []any{
				tenantA,
				tenantB,
				[]string{"knowledge.read", "plugin.read", "schedule.read", "store.read", "task.create", "task.read", "workspace.use"},
				[]string{"task.create", "task.read"},
			},
			want: 18,
		},
		{
			query: `SELECT count(*)
				FROM membership_roles AS assignment
				JOIN roles AS role ON role.tenant_id = assignment.tenant_id AND role.id = assignment.role_id
				WHERE (assignment.user_id = $1 AND assignment.tenant_id = $3 AND role.role_key = 'tenant_owner')
				   OR (assignment.user_id = $1 AND assignment.tenant_id = $4 AND role.role_key = 'tenant_member')
				   OR (assignment.user_id = $2 AND assignment.tenant_id = $3 AND role.role_key = 'tenant_member')
				   OR (assignment.user_id = $2 AND assignment.tenant_id = $4 AND role.role_key = 'tenant_owner')`,
			args: []any{userA, userB, tenantA, tenantB},
			want: 4,
		},
	}
	for index, check := range checks {
		var actual int
		if err := tx.QueryRow(ctx, check.query, check.args...).Scan(&actual); err != nil {
			return result, fmt.Errorf("%w: exact_matrix_check_%d", ErrUnavailable, index+1)
		}
		if actual != check.want {
			return result, ErrInvalidExecutionState
		}
	}

	var matchingAudits, revisionTwo, revisionThree, emptyDiff, distinctRequestIDs int
	if err := tx.QueryRow(ctx, `
		SELECT
			count(*) FILTER (
				WHERE tenant_id IN ($1, $2)
				  AND actor_type = 'operator'
				  AND actor_id = $3
				  AND resource_type = 'tenant'
				  AND resource_key IN ($4, $5)
				  AND outcome = 'success'
				  AND (SELECT count(*) FROM jsonb_object_keys(metadata)) = 7
				  AND metadata->>'schema_version' = '1'
				  AND metadata->>'data_classification' = 'synthetic_only'
				  AND metadata->>'role' IN ('tenant_owner', 'tenant_member')
				  AND metadata->>'reason' = 'none'
			),
			count(*) FILTER (WHERE metadata->>'authorization_revision' = '2'),
			count(*) FILTER (WHERE metadata->>'authorization_revision' = '3'),
			count(*) FILTER (WHERE metadata->'authorization_diff' = '[]'::jsonb),
			count(DISTINCT request_id)
		FROM audit_logs
		WHERE action = 'authorization.bootstrap'
	`,
		tenantA,
		tenantB,
		localLabActor,
		"feat-125-synthetic-bootstrap:"+localLabTenantA,
		"feat-125-synthetic-bootstrap:"+localLabTenantB,
	).Scan(&matchingAudits, &revisionTwo, &revisionThree, &emptyDiff, &distinctRequestIDs); err != nil {
		return result, fmt.Errorf("%w: exact_audit_check", ErrUnavailable)
	}
	if matchingAudits != 8 || revisionTwo != 2 || revisionThree != 6 || emptyDiff != 4 || distinctRequestIDs != 8 {
		return result, ErrInvalidExecutionState
	}

	if err := tx.Commit(ctx); err != nil {
		return result, ErrUnavailable
	}
	return result, nil
}
