package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, dsn string, minConns, maxConns int32) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres configuration: %w", err)
	}
	config.MinConns = minConns
	config.MaxConns = maxConns
	config.MaxConnIdleTime = 5 * time.Minute
	config.MaxConnLifetime = 30 * time.Minute
	config.HealthCheckPeriod = 30 * time.Second
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	config.ConnConfig.RuntimeParams["application_name"] = "yijie-api"

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func RequireSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var tasksTable, auditLogsTable, usersTable, identitiesTable, tenantsTable, membershipsTable *string
	var permissionsTable, rolesTable, rolePermissionsTable, membershipRolesTable *string
	var taskIdempotencyTable *string
	if err := pool.QueryRow(
		ctx,
		`SELECT
			to_regclass('tasks')::text,
			to_regclass('audit_logs')::text,
			to_regclass('users')::text,
			to_regclass('user_identities')::text,
			to_regclass('tenants')::text,
			to_regclass('tenant_memberships')::text,
			to_regclass('permissions')::text,
			to_regclass('roles')::text,
			to_regclass('role_permissions')::text,
			to_regclass('membership_roles')::text,
			to_regclass('task_create_idempotency')::text`,
	).Scan(
		&tasksTable,
		&auditLogsTable,
		&usersTable,
		&identitiesTable,
		&tenantsTable,
		&membershipsTable,
		&permissionsTable,
		&rolesTable,
		&rolePermissionsTable,
		&membershipRolesTable,
		&taskIdempotencyTable,
	); err != nil {
		return fmt.Errorf("check database schema: %w", err)
	}
	if tasksTable == nil || auditLogsTable == nil || usersTable == nil || identitiesTable == nil ||
		tenantsTable == nil || membershipsTable == nil || permissionsTable == nil || rolesTable == nil ||
		rolePermissionsTable == nil || membershipRolesTable == nil || taskIdempotencyTable == nil {
		return errors.New("database schema is not current; run migrations before starting the API")
	}
	return nil
}
