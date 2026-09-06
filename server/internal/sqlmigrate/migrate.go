// Package sqlmigrate is a minimal, versioned migration runner for the engine's
// per-service SQLite files.
//
// Each service owns exactly one SQLite file (single-writer, ADR-0016) and applies
// its own ordered migration list against that file. This helper only runs a
// caller-supplied, ordered slice of statements; it holds no schema and owns no
// data. Versioning uses PRAGMA user_version: the number of applied statements is
// recorded as the schema version, and later statements are applied in one
// transaction each.
package sqlmigrate

import (
	"context"
	"database/sql"
	"fmt"
)

// Migrate applies the ordered migration statements not yet applied to db.
// Applied count is tracked in PRAGMA user_version; each new statement runs in its
// own transaction so a failed statement leaves the schema at its prior version.
func Migrate(ctx context.Context, db *sql.DB, migrations []string) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	for i := current; i < len(migrations); i++ {
		stmt := migrations[i]
		if err := apply(ctx, db, i+1, stmt); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, version int, stmt string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, stmt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}
