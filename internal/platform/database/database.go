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
	var tasksTable, auditLogsTable *string
	if err := pool.QueryRow(
		ctx,
		"SELECT to_regclass('tasks')::text, to_regclass('audit_logs')::text",
	).Scan(&tasksTable, &auditLogsTable); err != nil {
		return fmt.Errorf("check database schema: %w", err)
	}
	if tasksTable == nil || auditLogsTable == nil {
		return errors.New("database schema is not current; run migrations before starting the API")
	}
	return nil
}
