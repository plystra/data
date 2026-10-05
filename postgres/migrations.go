package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/plystra/data/migration"
)

const migrationHistoryTable = "plystra_data_migrations"

var (
	ErrMigrationArtifact = errors.New("invalid migration artifact")
	ErrPlanApproval      = errors.New("migration plan approval required")
	ErrPlanChanged       = errors.New("migration plan changed")
)

// Artifact pairs one immutable migration definition with its selected
// PostgreSQL SQL artifact. The artifact digest must be DigestSQL(SQL).
type Artifact struct {
	Definition migration.Definition
	SQL        string
}

// DigestSQL returns the stable digest used to bind a migration artifact to
// the immutable definition supplied to ApplyMigrations.
func DigestSQL(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// MigrationPlan reads current target identity and migration history without
// creating or changing schema, then returns the canonical pending plan.
func (provider *Database) MigrationPlan(ctx context.Context, compiler string, artifacts []Artifact) (migration.Plan, error) {
	if ctx == nil || strings.TrimSpace(compiler) == "" {
		return migration.Plan{}, fmt.Errorf("%w: context and compiler are required", ErrMigrationArtifact)
	}
	definitions, err := artifactDefinitions(artifacts)
	if err != nil {
		return migration.Plan{}, err
	}
	target, err := provider.targetFingerprint(ctx)
	if err != nil {
		return migration.Plan{}, fmt.Errorf("migration target fingerprint: %w", err)
	}
	history, err := provider.readMigrationHistory(ctx)
	if err != nil {
		return migration.Plan{}, fmt.Errorf("migration history: %w", err)
	}
	return migration.Build(migration.Request{
		Target: target, Backend: "postgres/v1", Compiler: compiler,
		Definitions: definitions, History: history,
	})
}

// ApplyMigrations applies exactly one previously planned set. The caller must
// provide the exact plan digest; this value is checked again while holding a
// target-scoped PostgreSQL transaction lock before any schema statement runs.
func (provider *Database) ApplyMigrations(ctx context.Context, compiler, expectedPlanDigest string, artifacts []Artifact) (migration.Plan, error) {
	if ctx == nil || strings.TrimSpace(expectedPlanDigest) == "" {
		return migration.Plan{}, ErrPlanApproval
	}
	definitions, err := artifactDefinitions(artifacts)
	if err != nil {
		return migration.Plan{}, err
	}
	pool, err := provider.currentPool()
	if err != nil {
		return migration.Plan{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return migration.Plan{}, fmt.Errorf("migration transaction: %w", normalizeError(err, false))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	target, err := targetFingerprintTx(ctx, tx)
	if err != nil {
		return migration.Plan{}, fmt.Errorf("migration target fingerprint: %w", normalizeError(err, false))
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, target); err != nil {
		return migration.Plan{}, fmt.Errorf("migration target lock: %w", normalizeError(err, false))
	}
	history, err := readMigrationHistoryTx(ctx, tx)
	if err != nil {
		return migration.Plan{}, fmt.Errorf("migration history: %w", normalizeError(err, false))
	}
	plan, err := migration.Build(migration.Request{Target: target, Backend: "postgres/v1", Compiler: compiler, Definitions: definitions, History: history})
	if err != nil {
		return migration.Plan{}, err
	}
	if plan.PlanDigest != expectedPlanDigest {
		return migration.Plan{}, fmt.Errorf("%w: expected %s, current %s", ErrPlanChanged, expectedPlanDigest, plan.PlanDigest)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS plystra_data_migrations (
		applied_order bigserial PRIMARY KEY,
		member text NOT NULL,
		name text NOT NULL,
		version bigint NOT NULL,
		logical_digest text NOT NULL,
		artifact_digest text NOT NULL,
		compiler text NOT NULL,
		backend text NOT NULL,
		UNIQUE (member, name)
	)`); err != nil {
		return migration.Plan{}, fmt.Errorf("migration history table: %w", normalizeError(err, false))
	}
	byIdentity := make(map[migration.Identity]Artifact, len(artifacts))
	for _, artifact := range artifacts {
		byIdentity[artifact.Definition.Identity] = artifact
	}
	for _, definition := range plan.Pending {
		artifact := byIdentity[definition.Identity]
		if _, err := tx.Exec(ctx, artifact.SQL); err != nil {
			return migration.Plan{}, fmt.Errorf("migration artifact execution: %w", normalizeError(err, false))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plystra_data_migrations
			(member, name, version, logical_digest, artifact_digest, compiler, backend)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, definition.Identity.Member, definition.Identity.Name,
			definition.Version, definition.LogicalDigest, definition.ArtifactDigest, definition.Compiler, definition.Backend); err != nil {
			return migration.Plan{}, fmt.Errorf("migration history record: %w", normalizeError(err, false))
		}
		history = append(history, migration.HistoryEntry{
			Identity: definition.Identity, Version: definition.Version,
			LogicalDigest: definition.LogicalDigest, ArtifactDigest: definition.ArtifactDigest,
			Compiler: definition.Compiler, Backend: definition.Backend,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return migration.Plan{}, fmt.Errorf("migration commit: %w", normalizeError(err, true))
	}
	return migration.Build(migration.Request{Target: target, Backend: "postgres/v1", Compiler: compiler, Definitions: definitions, History: history})
}

func artifactDefinitions(artifacts []Artifact) ([]migration.Definition, error) {
	definitions := make([]migration.Definition, len(artifacts))
	for index, artifact := range artifacts {
		if strings.TrimSpace(artifact.SQL) == "" || artifact.Definition.ArtifactDigest != DigestSQL(artifact.SQL) {
			return nil, fmt.Errorf("%w: artifact %d", ErrMigrationArtifact, index)
		}
		definitions[index] = artifact.Definition
	}
	return definitions, nil
}

func (provider *Database) targetFingerprint(ctx context.Context) (string, error) {
	pool, err := provider.currentPool()
	if err != nil {
		return "", err
	}
	databaseName, databaseOID, err := targetDatabaseIdentity(pool.QueryRow(ctx, `SELECT current_database(), oid::text FROM pg_database WHERE datname = current_database()`))
	if err != nil {
		return "", err
	}
	systemIdentifier, err := targetSystemIdentifier(pool.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`))
	if err != nil {
		return "", err
	}
	return targetFingerprintValue(databaseName, databaseOID, systemIdentifier), nil
}

func targetFingerprintTx(ctx context.Context, tx pgx.Tx) (string, error) {
	databaseName, databaseOID, err := targetDatabaseIdentity(tx.QueryRow(ctx, `SELECT current_database(), oid::text FROM pg_database WHERE datname = current_database()`))
	if err != nil {
		return "", err
	}
	systemIdentifier, err := targetSystemIdentifier(tx.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`))
	if err != nil {
		return "", err
	}
	return targetFingerprintValue(databaseName, databaseOID, systemIdentifier), nil
}

type rowScanner interface{ Scan(...any) error }

func targetDatabaseIdentity(row rowScanner) (string, string, error) {
	var databaseName, databaseOID string
	if err := row.Scan(&databaseName, &databaseOID); err != nil {
		return "", "", err
	}
	return databaseName, databaseOID, nil
}

func targetSystemIdentifier(row rowScanner) (string, error) {
	var systemIdentifier string
	if err := row.Scan(&systemIdentifier); err != nil {
		return "", err
	}
	return systemIdentifier, nil
}

func targetFingerprintValue(databaseName, databaseOID, systemIdentifier string) string {
	payload := "plystra.data/postgres-target/v1|system=" + systemIdentifier + "|database_oid=" + databaseOID + "|database=" + databaseName
	sum := sha256.Sum256([]byte(payload))
	return "postgres/v1:sha256:" + hex.EncodeToString(sum[:])
}

func (provider *Database) readMigrationHistory(ctx context.Context) ([]migration.HistoryEntry, error) {
	pool, err := provider.currentPool()
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT member, name, version, logical_digest, artifact_digest, compiler, backend FROM plystra_data_migrations ORDER BY applied_order`)
	return readMigrationHistoryRows(ctx, rows, err)
}

func readMigrationHistoryTx(ctx context.Context, tx pgx.Tx) ([]migration.HistoryEntry, error) {
	rows, err := tx.Query(ctx, `SELECT member, name, version, logical_digest, artifact_digest, compiler, backend FROM plystra_data_migrations ORDER BY applied_order`)
	return readMigrationHistoryRows(ctx, rows, err)
}

type rowsScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

func readMigrationHistoryRows(_ context.Context, rows rowsScanner, queryErr error) ([]migration.HistoryEntry, error) {
	if queryErr != nil {
		var pgError *pgconn.PgError
		if errors.As(queryErr, &pgError) && pgError.Code == "42P01" {
			return nil, nil
		}
		return nil, queryErr
	}
	defer rows.Close()
	var history []migration.HistoryEntry
	for rows.Next() {
		var entry migration.HistoryEntry
		if err := rows.Scan(&entry.Identity.Member, &entry.Identity.Name, &entry.Version, &entry.LogicalDigest, &entry.ArtifactDigest, &entry.Compiler, &entry.Backend); err != nil {
			return nil, err
		}
		history = append(history, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return history, nil
}
