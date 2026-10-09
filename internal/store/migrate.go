// Package store owns Tallybook's Postgres schema: the migration runner that
// applies it and the query methods built on top of it.
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationFilename matches "0001_create_requests.sql": a zero-padded
// version number, an underscore, a descriptive name, and the .sql suffix.
var migrationFilename = regexp.MustCompile(`^(\d+)_([a-zA-Z0-9_]+)\.sql$`)

// Migration is one numbered schema change.
type Migration struct {
	Version  int
	Name     string
	Filename string
	SQL      string
}

// ErrNoMigrations is returned by ParseMigrations when fsys contains no
// files matching the numbered migration naming convention.
var ErrNoMigrations = errors.New("store: no migrations found")

// ParseMigrations reads every "NNNN_name.sql" file directly under fsys and
// returns them sorted by version, ascending. It rejects a filename that
// doesn't match the naming convention and a version number used twice —
// both indicate a mistake worth failing loudly on rather than silently
// misordering.
func ParseMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations dir: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		match := migrationFilename.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("store: migration file %q does not match the NNNN_name.sql naming convention", name)
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			// Unreachable given the regex, but handled rather than ignored.
			return nil, fmt.Errorf("store: migration file %q has an unparseable version: %w", name, err)
		}
		if prior, ok := seen[version]; ok {
			return nil, fmt.Errorf("store: migration version %d used twice: %q and %q", version, prior, name)
		}
		seen[version] = name

		sqlBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", name, err)
		}

		migrations = append(migrations, Migration{
			Version:  version,
			Name:     match[2],
			Filename: name,
			SQL:      string(sqlBytes),
		})
	}

	if len(migrations) == 0 {
		return nil, ErrNoMigrations
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

// createTrackingTableSQL creates the table the runner uses to record which
// migrations have already been applied. It is itself idempotent so Migrate
// can be called freely on every service startup.
const createTrackingTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

// Migrate applies every migration in migrations whose version is not yet
// recorded in schema_migrations, in ascending order, each in its own
// transaction alongside the bookkeeping row that marks it applied. It stops
// at the first failure — a schema change and its migrations row are never
// applied without each other, and no later migration runs on top of a
// database state that failed to reach the one before it.
//
// Migrate is safe to call on every service startup: migrations already
// recorded are skipped, so a fresh database and one that's already current
// both converge to the same schema.
func Migrate(ctx context.Context, pool *pgxpool.Pool, migrations []Migration) (newlyApplied []Migration, resultErr error) {
	// All services migrate the same database on startup. Keep a session lock
	// across the batch, including creation of the tracking table, so another
	// process cannot read stale bookkeeping and apply the same DDL.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("store: acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(724019832)`); err != nil {
		// A cancelled response can leave lock ownership uncertain.
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return nil, errors.Join(fmt.Errorf("store: lock migrations: %w", err), conn.Hijack().Close(closeCtx))
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(724019832)`); err != nil {
			// Never return a connection carrying a session lock to the pool.
			closeErr := conn.Hijack().Close(unlockCtx)
			resultErr = errors.Join(resultErr, fmt.Errorf("store: unlock migrations: %w", err), closeErr)
		}
	}()
	if _, err := conn.Exec(ctx, createTrackingTableSQL); err != nil {
		return nil, fmt.Errorf("store: create schema_migrations tracking table: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return newlyApplied, fmt.Errorf("store: begin transaction for migration %s: %w", m.Filename, err)
		}

		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return newlyApplied, fmt.Errorf("store: apply migration %s: %w", m.Filename, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name,
		); err != nil {
			_ = tx.Rollback(ctx)
			return newlyApplied, fmt.Errorf("store: record migration %s as applied: %w", m.Filename, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return newlyApplied, fmt.Errorf("store: commit migration %s: %w", m.Filename, err)
		}
		newlyApplied = append(newlyApplied, m)
	}

	return newlyApplied, nil
}

func appliedVersions(ctx context.Context, pool *pgxpool.Conn) (map[int]bool, error) {
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("store: scan applied migration version: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read applied migrations: %w", err)
	}
	return applied, nil
}
