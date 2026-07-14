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
