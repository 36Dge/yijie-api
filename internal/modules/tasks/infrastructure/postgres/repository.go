package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/36Dge/yijie-api/internal/modules/tasks/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

	var claimed bool
	err = tx.QueryRow(ctx, `
		INSERT INTO task_create_idempotency (
			tenant_id, created_by_user_id, idempotency_key, canonical_sha256
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, created_by_user_id, idempotency_key) DO NOTHING
		RETURNING true
	`, command.TenantID, command.UserID, command.IdempotencyKey, command.CanonicalSHA256).Scan(&claimed)
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
	`, command.TenantID, command.UserID, command.TaskType, command.Title, command.Input).Scan(
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

	var traceID *string
	if command.TraceID != "" {
		traceID = &command.TraceID
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO audit_logs (
			tenant_id, actor_type, actor_id, action, resource_type, resource_id,
			request_id, trace_id, outcome, metadata
		)
		VALUES ($1, 'user', $2, 'task.create', 'task', $3, $4, $5, 'success',
		        jsonb_build_object('authorization_mode', 'creator_private_v2'))
	`, command.TenantID, command.UserID, task.ID, command.RequestID, traceID); err != nil {
		return domain.Task{}, fmt.Errorf("insert secure task audit log: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Task{}, fmt.Errorf("commit secure create task transaction: %w", err)
	}
	return task, nil
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
