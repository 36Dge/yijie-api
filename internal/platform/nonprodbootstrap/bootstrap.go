package nonprodbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	authorizationdomain "github.com/36Dge/yijie-api/internal/modules/authorization/domain"
	tenancydomain "github.com/36Dge/yijie-api/internal/modules/tenancy/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SchemaVersion      = 1
	DataClassification = "synthetic_only"
	Environment        = "nonproduction"
	RoleTenantOwner    = "tenant_owner"
	RoleTenantMember   = "tenant_member"
	maxManifestBytes   = 32 * 1024
)

var (
	ErrInvalidInput     = errors.New("nonproduction bootstrap input is invalid")
	ErrConflict         = errors.New("nonproduction bootstrap conflicts with existing authorization state")
	ErrUnavailable      = errors.New("nonproduction bootstrap storage is unavailable")
	ErrAuditUnavailable = errors.New("nonproduction bootstrap failure audit is unavailable")
	ErrOutcomeUnknown   = errors.New("nonproduction bootstrap commit outcome is unknown")
)

type Manifest struct {
	SchemaVersion      int       `json:"schema_version"`
	DataClassification string    `json:"data_classification"`
	Issuer             string    `json:"issuer"`
	Subject            string    `json:"subject"`
	UserID             uuid.UUID `json:"user_id"`
	TenantID           uuid.UUID `json:"tenant_id"`
	TenantName         string    `json:"tenant_name"`
	Role               string    `json:"role"`
	ActorID            string    `json:"actor_id"`
}

type Result struct {
	RequestID             string    `json:"request_id"`
	StateChanged          bool      `json:"state_changed"`
	AuthorizationRevision int64     `json:"authorization_revision"`
	AuthorizationDiff     []string  `json:"authorization_diff"`
	UserID                uuid.UUID `json:"user_id"`
	TenantID              uuid.UUID `json:"tenant_id"`
	Role                  string    `json:"role"`
}

type Runner struct {
	pool           *pgxpool.Pool
	environment    string
	expectedIssuer string
	commit         func(context.Context, pgx.Tx) error
}

type applyResult struct {
	stateChanged          bool
	authorizationRevision int64
	authorizationDiff     []string
}

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type roleDefinition struct {
	key         string
	name        string
	permissions []authorizationdomain.Permission
}

var systemRoles = [...]roleDefinition{
	{
		key:         RoleTenantOwner,
		name:        "Tenant owner",
		permissions: authorizationdomain.DefaultProductPermissions(),
	},
	{
		key:         RoleTenantMember,
		name:        "Tenant member",
		permissions: []authorizationdomain.Permission{authorizationdomain.TaskCreate, authorizationdomain.TaskRead, authorizationdomain.WebRetrieve},
	},
}

func DecodeManifest(reader io.Reader) (Manifest, error) {
	if reader == nil {
		return Manifest{}, ErrInvalidInput
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	if err != nil || len(data) > maxManifestBytes {
		return Manifest{}, ErrInvalidInput
	}
	if err := rejectDuplicateTopLevelKeys(data); err != nil {
		return Manifest{}, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, ErrInvalidInput
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Manifest{}, ErrInvalidInput
	}
	return manifest, nil
}

func (manifest Manifest) Validate(environment, expectedIssuer string) error {
	if environment != Environment || manifest.SchemaVersion != SchemaVersion ||
		manifest.DataClassification != DataClassification {
		return ErrInvalidInput
	}
	if manifest.UserID == uuid.Nil || manifest.TenantID == uuid.Nil || manifest.UserID == manifest.TenantID {
		return ErrInvalidInput
	}
	if !validIssuer(expectedIssuer) || manifest.Issuer != expectedIssuer || !validSubject(manifest.Subject) {
		return ErrInvalidInput
	}
	if manifest.TenantName != strings.TrimSpace(manifest.TenantName) ||
		!strings.HasPrefix(manifest.TenantName, "Synthetic ") ||
		utf8.RuneCountInString(manifest.TenantName) > 200 || hasControl(manifest.TenantName) {
		return ErrInvalidInput
	}
	if manifest.Role != RoleTenantOwner && manifest.Role != RoleTenantMember {
		return ErrInvalidInput
	}
	if manifest.ActorID != strings.TrimSpace(manifest.ActorID) ||
		!strings.HasPrefix(manifest.ActorID, "nonprod:") ||
		len(manifest.ActorID) == len("nonprod:") ||
		utf8.RuneCountInString(manifest.ActorID) > 200 || hasControl(manifest.ActorID) {
		return ErrInvalidInput
	}
	return nil
}

func New(pool *pgxpool.Pool, environment, expectedIssuer string) *Runner {
	return &Runner{
		pool:           pool,
		environment:    environment,
		expectedIssuer: expectedIssuer,
		commit:         func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) },
	}
}

func (runner *Runner) Run(ctx context.Context, manifest Manifest) (Result, error) {
	requestID := "feat-125-bootstrap-" + uuid.NewString()
	result := Result{
		RequestID: requestID,
		UserID:    manifest.UserID,
		TenantID:  manifest.TenantID,
		Role:      manifest.Role,
	}
	if runner == nil {
		return result, ErrUnavailable
	}
	if err := manifest.Validate(runner.environment, runner.expectedIssuer); err != nil {
		return result, err
	}
	if runner.pool == nil {
		return result, ErrUnavailable
	}

	tx, err := runner.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return result, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	applied, applyErr := apply(ctx, tx, manifest)
	if applyErr != nil {
		_ = tx.Rollback(ctx)
		return result, recordFailure(runner.pool, manifest, requestID, reasonCode(applyErr), applyErr)
	}
	if err := writeAudit(
		ctx,
		tx,
		manifest,
		requestID,
		"success",
		applied.stateChanged,
		"none",
		&applied.authorizationRevision,
		applied.authorizationDiff,
	); err != nil {
		_ = tx.Rollback(ctx)
		return result, recordFailure(runner.pool, manifest, requestID, "audit_unavailable", ErrUnavailable)
	}
	commit := runner.commit
	if commit == nil {
		commit = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }
	}
	if err := commit(ctx, tx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return result, recordFailure(runner.pool, manifest, requestID, "commit_rolled_back", ErrUnavailable)
		}
		if committedAuditExists(runner.pool, manifest, requestID, applied) {
			result.StateChanged = applied.stateChanged
			result.AuthorizationRevision = applied.authorizationRevision
			result.AuthorizationDiff = applied.authorizationDiff
			return result, nil
		}
		return result, ErrOutcomeUnknown
	}
	result.StateChanged = applied.stateChanged
	result.AuthorizationRevision = applied.authorizationRevision
	result.AuthorizationDiff = applied.authorizationDiff
	return result, nil
}

// RunClosedMatrix applies one already-validated four-manifest matrix in a
// single transaction. Failed execution cannot leave partial authorization or
// bootstrap-audit state.
func (runner *Runner) RunClosedMatrix(ctx context.Context, manifests []Manifest) ([]Result, error) {
	results := make([]Result, len(manifests))
	if runner == nil || len(manifests) != 4 {
		return results, ErrUnavailable
	}
	for index, manifest := range manifests {
		results[index] = Result{
			RequestID: "feat-125-bootstrap-" + uuid.NewString(),
			UserID:    manifest.UserID,
			TenantID:  manifest.TenantID,
			Role:      manifest.Role,
		}
		if err := manifest.Validate(runner.environment, runner.expectedIssuer); err != nil {
			return results, err
		}
	}
	if runner.pool == nil {
		return results, ErrUnavailable
	}

	tx, err := runner.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return results, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	appliedResults := make([]applyResult, len(manifests))
	for index, manifest := range manifests {
		applied, applyErr := apply(ctx, tx, manifest)
		if applyErr != nil {
			_ = tx.Rollback(ctx)
			return results, applyErr
		}
		if err := writeAudit(
			ctx,
			tx,
			manifest,
			results[index].RequestID,
			"success",
			applied.stateChanged,
			"none",
			&applied.authorizationRevision,
			applied.authorizationDiff,
		); err != nil {
			_ = tx.Rollback(ctx)
			return results, ErrUnavailable
		}
		appliedResults[index] = applied
		results[index].StateChanged = applied.stateChanged
		results[index].AuthorizationRevision = applied.authorizationRevision
		results[index].AuthorizationDiff = applied.authorizationDiff
	}

	commit := runner.commit
	if commit == nil {
		commit = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }
	}
	if err := commit(ctx, tx); err != nil {
		if errors.Is(err, pgx.ErrTxCommitRollback) {
			return results, ErrUnavailable
		}
		for index, manifest := range manifests {
			if !committedAuditExists(runner.pool, manifest, results[index].RequestID, appliedResults[index]) {
				return results, ErrOutcomeUnknown
			}
		}
	}
	return results, nil
}

func committedAuditExists(
	pool *pgxpool.Pool,
	manifest Manifest,
	requestID string,
	applied applyResult,
) bool {
	if pool == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var matches int
	err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM audit_logs
		WHERE tenant_id = $1
		  AND actor_type = 'operator'
		  AND actor_id = $2
		  AND action = 'authorization.bootstrap'
		  AND resource_type = 'tenant'
		  AND resource_key = $3
		  AND request_id = $4
		  AND outcome = 'success'
		  AND metadata->>'schema_version' = '1'
		  AND metadata->>'data_classification' = 'synthetic_only'
		  AND metadata->>'role' = $5
		  AND (metadata->>'state_changed')::boolean = $6
		  AND metadata->>'reason' = 'none'
		  AND (metadata->>'authorization_revision')::bigint = $7
		  AND metadata->'authorization_diff' = to_jsonb($8::text[])
	`,
		manifest.TenantID,
		manifest.ActorID,
		"feat-125-synthetic-bootstrap:"+manifest.TenantID.String(),
		requestID,
		manifest.Role,
		applied.stateChanged,
		applied.authorizationRevision,
		applied.authorizationDiff,
	).Scan(&matches)
	return err == nil && matches == 1
}

func apply(ctx context.Context, tx pgx.Tx, manifest Manifest) (applyResult, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "feat-125-identity:"+manifest.Issuer+":"+manifest.Subject); err != nil {
		return applyResult{}, ErrUnavailable
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "feat-125-tenant:"+manifest.TenantID.String()); err != nil {
		return applyResult{}, ErrUnavailable
	}

	stateChanged := false
	for _, operation := range []func(context.Context, pgx.Tx, Manifest) (bool, error){
		ensureUser,
		ensureIdentity,
		ensureTenant,
	} {
		operationChanged, err := operation(ctx, tx, manifest)
		if err != nil {
			return applyResult{}, err
		}
		stateChanged = stateChanged || operationChanged
	}

	authorizationDiff := make([]string, 0, 16)
	membershipChanged, err := ensureMembership(ctx, tx, manifest)
	if err != nil {
		return applyResult{}, err
	}
	if membershipChanged {
		authorizationDiff = append(authorizationDiff, "membership.created")
	}

	roles := make(map[string]uuid.UUID, len(systemRoles))
	for _, definition := range systemRoles {
		roleID, roleDiff, err := ensureRole(ctx, tx, manifest.TenantID, definition)
		if err != nil {
			return applyResult{}, err
		}
		roles[definition.key] = roleID
		authorizationDiff = append(authorizationDiff, roleDiff...)
	}

	assignmentChanged, err := ensureAssignment(ctx, tx, manifest, roles)
	if err != nil {
		return applyResult{}, err
	}
	if assignmentChanged {
		authorizationDiff = append(authorizationDiff, "assignment.created:"+manifest.Role)
	}

	revision, err := authorizationRevision(ctx, tx, manifest.TenantID, len(authorizationDiff) != 0)
	if err != nil {
		return applyResult{}, err
	}
	return applyResult{
		stateChanged:          stateChanged || len(authorizationDiff) != 0,
		authorizationRevision: revision,
		authorizationDiff:     authorizationDiff,
	}, nil
}

func ensureUser(ctx context.Context, tx pgx.Tx, manifest Manifest) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO users (id, status)
		VALUES ($1, 'active')
		ON CONFLICT (id) DO NOTHING
	`, manifest.UserID)
	if err != nil {
		return false, ErrUnavailable
	}
	var status string
	if err := tx.QueryRow(ctx, "SELECT status FROM users WHERE id = $1", manifest.UserID).Scan(&status); err != nil {
		return false, ErrUnavailable
	}
	if status != "active" {
		return false, ErrConflict
	}
	return tag.RowsAffected() == 1, nil
}

func ensureIdentity(ctx context.Context, tx pgx.Tx, manifest Manifest) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO user_identities (user_id, issuer, subject)
		VALUES ($1, $2, $3)
		ON CONFLICT (issuer, subject) DO NOTHING
	`, manifest.UserID, manifest.Issuer, manifest.Subject)
	if err != nil {
		return false, ErrUnavailable
	}
	var userID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT user_id FROM user_identities WHERE issuer = $1 AND subject = $2
	`, manifest.Issuer, manifest.Subject).Scan(&userID); err != nil {
		return false, ErrUnavailable
	}
	if userID != manifest.UserID {
		return false, ErrConflict
	}
	return tag.RowsAffected() == 1, nil
}

func ensureTenant(ctx context.Context, tx pgx.Tx, manifest Manifest) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO tenants (id, name, status)
		VALUES ($1, $2, 'active')
		ON CONFLICT (id) DO NOTHING
	`, manifest.TenantID, manifest.TenantName)
	if err != nil {
		return false, ErrUnavailable
	}
	var name, status string
	if err := tx.QueryRow(ctx, "SELECT name, status FROM tenants WHERE id = $1", manifest.TenantID).Scan(&name, &status); err != nil {
		return false, ErrUnavailable
	}
	if name != manifest.TenantName || status != "active" {
		return false, ErrConflict
	}
	return tag.RowsAffected() == 1, nil
}

func ensureMembership(ctx context.Context, tx pgx.Tx, manifest Manifest) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO tenant_memberships (tenant_id, user_id, status)
		VALUES ($1, $2, 'active')
		ON CONFLICT (tenant_id, user_id) DO NOTHING
	`, manifest.TenantID, manifest.UserID)
	if err != nil {
		return false, ErrUnavailable
	}
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM tenant_memberships WHERE tenant_id = $1 AND user_id = $2
	`, manifest.TenantID, manifest.UserID).Scan(&status); err != nil {
		return false, ErrUnavailable
	}
	if status != "active" {
		return false, ErrConflict
	}
	return tag.RowsAffected() == 1, nil
}

func ensureRole(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, definition roleDefinition) (uuid.UUID, []string, error) {
	var roleID uuid.UUID
	var name string
	var system bool
	err := tx.QueryRow(ctx, `
		SELECT id, name, is_system FROM roles WHERE tenant_id = $1 AND role_key = $2
	`, tenantID, definition.key).Scan(&roleID, &name, &system)
	diff := make([]string, 0, len(definition.permissions)+1)
	if errors.Is(err, pgx.ErrNoRows) {
		roleID = uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO roles (id, tenant_id, role_key, name, is_system)
			VALUES ($1, $2, $3, $4, true)
		`, roleID, tenantID, definition.key, definition.name); err != nil {
			return uuid.Nil, nil, ErrUnavailable
		}
		name = definition.name
		system = true
		diff = append(diff, "role.created:"+definition.key)
	} else if err != nil {
		return uuid.Nil, nil, ErrUnavailable
	}
	if name != definition.name || !system {
		return uuid.Nil, nil, ErrConflict
	}

	for _, permission := range definition.permissions {
		tag, err := tx.Exec(ctx, `
			INSERT INTO role_permissions (tenant_id, role_id, permission_key)
			VALUES ($1, $2, $3)
			ON CONFLICT (tenant_id, role_id, permission_key) DO NOTHING
		`, tenantID, roleID, permission)
		if err != nil {
			return uuid.Nil, nil, ErrUnavailable
		}
		if tag.RowsAffected() == 1 {
			diff = append(diff, "role_permission.created:"+definition.key+":"+string(permission))
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT permission_key
		FROM role_permissions
		WHERE tenant_id = $1 AND role_id = $2
		ORDER BY permission_key
	`, tenantID, roleID)
	if err != nil {
		return uuid.Nil, nil, ErrUnavailable
	}
	permissions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return uuid.Nil, nil, ErrUnavailable
	}
	expected := make([]string, len(definition.permissions))
	for index, permission := range definition.permissions {
		expected[index] = string(permission)
	}
	slices.Sort(expected)
	if !slices.Equal(permissions, expected) {
		return uuid.Nil, nil, ErrConflict
	}
	return roleID, diff, nil
}

func authorizationRevision(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, changed bool) (int64, error) {
	var revision int64
	if !changed {
		if err := tx.QueryRow(ctx, `
			SELECT authorization_revision
			FROM tenants
			WHERE id = $1
		`, tenantID).Scan(&revision); err != nil {
			return 0, ErrUnavailable
		}
		return revision, nil
	}
	if err := tx.QueryRow(ctx, `
		UPDATE tenants
		SET authorization_revision = authorization_revision + 1,
		    updated_at = now()
		WHERE id = $1
		  AND authorization_revision < $2
		RETURNING authorization_revision
	`, tenantID, tenancydomain.MaxAuthorizationRevision).Scan(&revision); errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrConflict
	} else if err != nil {
		return 0, ErrUnavailable
	}
	return revision, nil
}

func ensureAssignment(ctx context.Context, tx pgx.Tx, manifest Manifest, roles map[string]uuid.UUID) (bool, error) {
	var otherAssignments int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM membership_roles AS assignment
		JOIN roles AS role
		  ON role.tenant_id = assignment.tenant_id
		 AND role.id = assignment.role_id
		WHERE assignment.tenant_id = $1
		  AND assignment.user_id = $2
		  AND role.role_key IN ('tenant_owner', 'tenant_member')
		  AND role.role_key <> $3
	`, manifest.TenantID, manifest.UserID, manifest.Role).Scan(&otherAssignments); err != nil {
		return false, ErrUnavailable
	}
	if otherAssignments != 0 {
		return false, ErrConflict
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO membership_roles (tenant_id, user_id, role_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, user_id, role_id) DO NOTHING
	`, manifest.TenantID, manifest.UserID, roles[manifest.Role])
	if err != nil {
		return false, ErrUnavailable
	}
	return tag.RowsAffected() == 1, nil
}

func writeAudit(
	ctx context.Context,
	db executor,
	manifest Manifest,
	requestID string,
	outcome string,
	stateChanged bool,
	reason string,
	authorizationRevision *int64,
	authorizationDiff []string,
) error {
	_, err := db.Exec(ctx, `
		INSERT INTO audit_logs (
			tenant_id, actor_type, actor_id, action, resource_type, resource_key,
			request_id, outcome, metadata
		) VALUES (
			$1, 'operator', $2, 'authorization.bootstrap', 'tenant', $3,
			$4, $5, jsonb_build_object(
				'schema_version', 1,
				'data_classification', 'synthetic_only',
				'role', $6::text,
				'state_changed', $7::boolean,
				'reason', $8::text,
				'authorization_revision', $9::bigint,
				'authorization_diff', to_jsonb($10::text[])
			)
		)
	`,
		manifest.TenantID,
		manifest.ActorID,
		"feat-125-synthetic-bootstrap:"+manifest.TenantID.String(),
		requestID,
		outcome,
		manifest.Role,
		stateChanged,
		reason,
		authorizationRevision,
		authorizationDiff,
	)
	return err
}

func reasonCode(err error) string {
	if errors.Is(err, ErrConflict) {
		return "state_conflict"
	}
	return "storage_unavailable"
}

func recordFailure(
	pool *pgxpool.Pool,
	manifest Manifest,
	requestID string,
	reason string,
	primary error,
) error {
	auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auditErr := writeAudit(auditCtx, pool, manifest, requestID, "failure", false, reason, nil, []string{})
	return composeFailure(primary, auditErr)
}

func composeFailure(primary, auditErr error) error {
	if auditErr == nil {
		return primary
	}
	return errors.Join(primary, ErrAuditUnavailable)
}

func rejectDuplicateTopLevelKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return ErrInvalidInput
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return ErrInvalidInput
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidInput
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return ErrInvalidInput
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidInput
	}
	return nil
}

func validIssuer(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) || len(raw) > 1024 || hasControl(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.IsAbs() && parsed.Scheme == "https" && parsed.Hostname() != "" &&
		parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validSubject(subject string) bool {
	return subject != "" && subject == strings.TrimSpace(subject) && len(subject) <= 512 && !hasControl(subject)
}

func hasControl(value string) bool {
	return strings.ContainsFunc(value, unicode.IsControl)
}
