package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed sql/*.sql
var files embed.FS

const LatestVersion int64 = 4

func Run(ctx context.Context, dsn, command string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping migration database: %w", err)
	}

	goose.SetBaseFS(files)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}

	var migrationErr error
	switch command {
	case "up":
		migrationErr = goose.UpContext(ctx, db, "sql")
	case "down":
		migrationErr = goose.DownContext(ctx, db, "sql")
	case "status":
		migrationErr = goose.StatusContext(ctx, db, "sql")
	default:
		return fmt.Errorf("unsupported migration command %q", command)
	}
	if migrationErr != nil {
		return fmt.Errorf("run migration %s: %w", command, migrationErr)
	}
	return nil
}

func RequireCurrent(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration database: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping migration database: %w", err)
	}
	version, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return fmt.Errorf("read database migration version: %w", err)
	}
	if version != LatestVersion {
		return fmt.Errorf("database migration version is %d, expected %d", version, LatestVersion)
	}
	return nil
}
