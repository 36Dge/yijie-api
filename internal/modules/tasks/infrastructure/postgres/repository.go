package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	secureTaskIdempotencyRetention = 24 * time.Hour
	secureTaskIdempotencyBatch     = 100
	secureTaskInternalTitle        = "local-only-conversation"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

func (r *Repository) Create(ctx context.Context, command domain.CreateTask) (task domain.Task, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Task{}, fmt.Errorf("begin create task transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("rollback create task transaction: %w", rollbackErr)
		}
	}()

	err = tx.QueryRow(ctx, `
		INSERT INTO tasks (tenant_id, task_type, title, input)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, task_type, title, status, input, result, error_message, created_at, updated_at
	`, command.TenantID, command.TaskType, command.Title, command.Input).Scan(
		&task.ID,
		&task.TenantID,
		&task.TaskType,
		&task.Title,
		&task.Status,
		&task.Input,
		&task.Result,
		&task.ErrorMessage,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if err != nil {
		return domain.Task{}, fmt.Errorf("insert task: %w", err)
	}

	var traceID *string
	if command.TraceID != "" {
		traceID = &command.TraceID
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (
			tenant_id, actor_type, action, resource_type, resource_id,
			request_id, trace_id, outcome, metadata
		)
		VALUES ($1, 'client', 'task.create', 'task', $2, $3, $4, 'success', jsonb_build_object('task_type', $5::text))
	`, command.TenantID, task.ID, command.RequestID, traceID, command.TaskType)
	if err != nil {
		return domain.Task{}, fmt.Errorf("insert task audit log: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Task{}, fmt.Errorf("commit create task transaction: %w", err)
	}
	return task, nil
}

func (r *Repository) Get(ctx context.Context, id string) (domain.Task, error) {
	var task domain.Task
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, task_type, title, status, input, result, error_message, created_at, updated_at
		FROM tasks
		WHERE id = $1
	`, id).Scan(
		&task.ID,
		&task.TenantID,
		&task.TaskType,
		&task.Title,
		&task.Status,
		&task.Input,
		&task.Result,
		&task.ErrorMessage,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("get task: %w", err)
	}
	return task, nil
}

func (r *Repository) CreateSecure(ctx context.Context, command domain.CreateSecureTask) (task domain.Task, err error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return domain.Task{}, fmt.Errorf("begin secure create task transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("rollback secure create task transaction: %w", rollbackErr)
		}
	}()

	if _, err = tx.Exec(ctx, `
		DELETE FROM task_create_idempotency
		WHERE tenant_id = $1 AND created_by_user_id = $2 AND idempotency_key = $3
		  AND expires_at <= now()
	`, command.TenantID, command.UserID, command.IdempotencyKey); err != nil {
		return domain.Task{}, fmt.Errorf("expire secure task idempotency key: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		WITH expired AS (
			SELECT tenant_id, created_by_user_id, idempotency_key
			FROM task_create_idempotency
			WHERE expires_at <= now()
			ORDER BY expires_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM task_create_idempotency AS record
		USING expired
		WHERE record.tenant_id = expired.tenant_id
		  AND record.created_by_user_id = expired.created_by_user_id
		  AND record.idempotency_key = expired.idempotency_key
	`, secureTaskIdempotencyBatch); err != nil {
		return domain.Task{}, fmt.Errorf("cleanup expired secure task idempotency records: %w", err)
	}

	var claimed bool
	err = tx.QueryRow(ctx, `
		INSERT INTO task_create_idempotency (
			tenant_id, created_by_user_id, idempotency_key, canonical_sha256, expires_at
		)
		VALUES ($1, $2, $3, $4, now() + $5::interval)
		ON CONFLICT (tenant_id, created_by_user_id, idempotency_key) DO NOTHING
		RETURNING true
	`, command.TenantID, command.UserID, command.IdempotencyKey, command.CanonicalSHA256, secureTaskIdempotencyRetention.String()).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		var storedDigest string
		var taskID *string
		err = tx.QueryRow(ctx, `
			SELECT canonical_sha256, task_id
			FROM task_create_idempotency
			WHERE tenant_id = $1 AND created_by_user_id = $2 AND idempotency_key = $3
		`, command.TenantID, command.UserID, command.IdempotencyKey).Scan(&storedDigest, &taskID)
		if err != nil {
			return domain.Task{}, fmt.Errorf("read secure task idempotency record: %w", err)
		}
		if storedDigest != command.CanonicalSHA256 || taskID == nil {
			return domain.Task{}, domain.ErrIdempotencyConflict
		}
		task, err = getSecureWithQuerier(ctx, tx, command.SecureScope, *taskID)
		if err != nil {
			return domain.Task{}, err
		}
		if err = insertSecureTaskAudit(ctx, tx, command, task.ID, httpStatusCreated); err != nil {
			return domain.Task{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Task{}, fmt.Errorf("commit idempotent secure task read: %w", err)
		}
		return task, nil
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("claim secure task idempotency key: %w", err)
	}
	if !claimed {
		return domain.Task{}, domain.ErrIdempotencyConflict
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO tasks (tenant_id, created_by_user_id, task_type, title, input)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, tenant_id, created_by_user_id, task_type, title, status,
		          input, result, error_message, created_at, updated_at
	`, command.TenantID, command.UserID, command.TaskType, secureTaskInternalTitle, command.Input).Scan(
		&task.ID,
		&task.TenantID,
		&task.CreatedByUserID,
		&task.TaskType,
		&task.Title,
		&task.Status,
		&task.Input,
		&task.Result,
		&task.ErrorMessage,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if err != nil {
		return domain.Task{}, fmt.Errorf("insert secure task: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		UPDATE task_create_idempotency
		SET task_id = $4
		WHERE tenant_id = $1 AND created_by_user_id = $2 AND idempotency_key = $3
	`, command.TenantID, command.UserID, command.IdempotencyKey, task.ID); err != nil {
		return domain.Task{}, fmt.Errorf("bind secure task idempotency result: %w", err)
	}

	if err = insertSecureTaskAudit(ctx, tx, command, task.ID, httpStatusCreated); err != nil {
		return domain.Task{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Task{}, fmt.Errorf("commit secure create task transaction: %w", err)
	}
	return task, nil
}

const httpStatusCreated = 201

func insertSecureTaskAudit(ctx context.Context, tx pgx.Tx, command domain.CreateSecureTask, taskID string, statusCode int) error {
	var traceID *string
	if command.TraceID != "" {
		traceID = &command.TraceID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_logs (
			tenant_id, actor_type, actor_id, action, resource_type, resource_id,
			request_id, trace_id, outcome, metadata
		)
		VALUES ($1, 'user', $2, 'task.create', 'task', $3, $4, $5, 'success',
		        jsonb_build_object(
		          'authorization_mode', 'creator_private_v2',
		          'status_code', $6::integer,
		          'reason_code', 'success'
		        ))
	`, command.TenantID, command.UserID, taskID, command.RequestID, traceID, statusCode); err != nil {
		return fmt.Errorf("insert secure task audit log: %w", err)
	}
	return nil
}

func (r *Repository) RecordSecureAudit(ctx context.Context, audit domain.SecureTaskAudit) error {
	if r == nil || r.pool == nil {
		return domain.ErrUnavailable
	}
	var tenantID, userID, taskID, resourceKey, traceID any
	if parsed, err := uuid.Parse(audit.TenantID); err == nil && parsed != uuid.Nil {
		tenantID = parsed
	}
	if parsed, err := uuid.Parse(audit.UserID); err == nil && parsed != uuid.Nil {
		userID = parsed.String()
	}
	if parsed, err := uuid.Parse(audit.TaskID); err == nil && parsed != uuid.Nil {
		taskID = parsed
	} else {
		resourceKey = audit.RequestID
	}
	if audit.TraceID != "" {
		traceID = audit.TraceID
	}
	actorType := "anonymous"
	if userID != nil {
		actorType = "user"
	}
	outcome := "failure"
	if audit.Outcome == "success" {
		outcome = "success"
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO audit_logs (
			tenant_id, actor_type, actor_id, action, resource_type, resource_id,
			resource_key, request_id, trace_id, outcome, metadata
		)
		VALUES ($1, $2, $3, $4, 'task', $5, $6, $7, $8, $9,
		        jsonb_build_object(
		          'authorization_mode', 'creator_private_v2',
		          'status_code', $10::integer,
		          'reason_code', $11::text,
		          'scope_state', CASE WHEN $1::uuid IS NULL THEN 'unresolved' ELSE 'verified' END
		        ))
	`, tenantID, actorType, userID, audit.Operation, taskID, resourceKey, audit.RequestID, traceID, outcome, audit.StatusCode, audit.Code); err != nil {
		return fmt.Errorf("insert secure task request audit: %w", err)
	}
	return nil
}

func (r *Repository) CleanupExpiredSecureIdempotency(ctx context.Context, limit int) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, domain.ErrUnavailable
	}
	if limit < 1 || limit > 1000 {
		return 0, domain.ErrInvalid
	}
	result, err := r.pool.Exec(ctx, `
		WITH expired AS (
			SELECT tenant_id, created_by_user_id, idempotency_key
			FROM task_create_idempotency
			WHERE expires_at <= now()
			ORDER BY expires_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM task_create_idempotency AS record
		USING expired
		WHERE record.tenant_id = expired.tenant_id
		  AND record.created_by_user_id = expired.created_by_user_id
		  AND record.idempotency_key = expired.idempotency_key
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("cleanup expired secure task idempotency records: %w", err)
	}
	return result.RowsAffected(), nil
}

func (r *Repository) GetSecure(ctx context.Context, scope domain.SecureScope, id string) (domain.Task, error) {
	return getSecureWithQuerier(ctx, r.pool, scope, id)
}

type secureTaskQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getSecureWithQuerier(ctx context.Context, querier secureTaskQuerier, scope domain.SecureScope, id string) (domain.Task, error) {
	var task domain.Task
	err := querier.QueryRow(ctx, `
		SELECT id, tenant_id, created_by_user_id, task_type, title, status,
		       input, result, error_message, created_at, updated_at
		FROM tasks
		WHERE id = $1 AND tenant_id = $2 AND created_by_user_id = $3
	`, id, scope.TenantID, scope.UserID).Scan(
		&task.ID,
		&task.TenantID,
		&task.CreatedByUserID,
		&task.TaskType,
		&task.Title,
		&task.Status,
		&task.Input,
		&task.Result,
		&task.ErrorMessage,
		&task.CreatedAt,
		&task.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Task{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Task{}, fmt.Errorf("get secure task: %w", err)
	}
	return task, nil
}
