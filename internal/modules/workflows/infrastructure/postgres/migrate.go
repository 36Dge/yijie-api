package postgres

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"

	"github.com/36Dge/yijie-api/internal/modules/workflows/domain"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate is called by the explicit migration command only, never server startup.
func Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return domain.Fail("storage_unavailable")
	}
	defer db.Close()
	files, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return domain.Fail("internal_error")
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, files, goose.WithTableName("workflow_schema_version"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return domain.Fail("storage_unavailable")
	}
	if _, err = p.Up(ctx); err != nil {
		return domain.Fail("storage_unavailable")
	}
	return nil
}
