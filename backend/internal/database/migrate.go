package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	migrationfiles "github.com/Amsors/Bosun/backend/db/migrations"
)

// Migrate applies all embedded migrations before the API begins serving.
func Migrate(ctx context.Context, databaseURL string, timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("migration timeout must be positive")
	}

	migrationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	connConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse migration database URL: %w", err)
	}
	sqlDB := stdlib.OpenDB(*connConfig)
	defer func() {
		_ = sqlDB.Close()
	}()
	if err := sqlDB.PingContext(migrationCtx); err != nil {
		return fmt.Errorf("connect migration database: %w", err)
	}

	source, err := iofs.New(migrationfiles.Files, ".")
	if err != nil {
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	driver, err := pgxdriver.WithInstance(sqlDB, &pgxdriver.Config{
		MigrationsTable:  "schema_migrations",
		SchemaName:       "public",
		StatementTimeout: timeout,
	})
	if err != nil {
		return fmt.Errorf("create migration database driver: %w", err)
	}
	runner, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("create migration runner: %w", err)
	}
	defer func() {
		_, _ = runner.Close()
	}()

	stopDone := make(chan struct{})
	go func() {
		select {
		case <-migrationCtx.Done():
			select {
			case runner.GracefulStop <- true:
			default:
			}
		case <-stopDone:
		}
	}()

	err = runner.Up()
	close(stopDone)
	if migrationCtx.Err() != nil {
		return fmt.Errorf("database migration timed out: %w", migrationCtx.Err())
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply database migrations: %w", err)
	}
	return nil
}
