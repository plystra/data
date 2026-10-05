package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	db "github.com/plystra/data/database"
	"github.com/plystra/kernel/configuration"
)

var (
	// ErrInvalidConfig reports an invalid provider configuration.
	ErrInvalidConfig = errors.New("invalid PostgreSQL configuration")
	// ErrState reports an invalid provider lifecycle transition.
	ErrState = errors.New("invalid PostgreSQL provider state")
)

// Config owns one PostgreSQL Resource instance. The URL is a Kernel Secret so
// credentials never enter generated artifacts or public provenance.
type Config struct {
	URL            configuration.Secret `yaml:"url" plystra:"required"`
	MaxConnections int32                `yaml:"max_connections" plystra:"required"`
	MinConnections int32                `yaml:"min_connections"`
	HealthTimeout  time.Duration        `yaml:"health_timeout" plystra-default:"5s"`
}

// Database owns one pool and shares it across every bound consumer.
type Database struct {
	mu     sync.RWMutex
	config Config
	pool   *pgxpool.Pool
	start  bool
}

//plystra:implements-resource data.database/v1
func New(config Config) (*Database, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return &Database{config: config}, nil
}

func validateConfig(config Config) error {
	if !config.URL.Valid() || config.MaxConnections <= 0 || config.MinConnections < 0 || config.MinConnections > config.MaxConnections || config.HealthTimeout <= 0 {
		return ErrInvalidConfig
	}
	parsed, err := url.Parse(string(config.URL.Bytes()))
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Host == "" {
		return ErrInvalidConfig
	}
	return nil
}

// Start opens and health-checks the pool. Constructors only validate and store
// typed values; network acquisition belongs to this lifecycle hook.
func (provider *Database) Start(ctx context.Context) error {
	if provider == nil || ctx == nil {
		return ErrState
	}
	provider.mu.Lock()
	if provider.start || provider.pool != nil {
		provider.mu.Unlock()
		return ErrState
	}
	provider.start = true
	config := provider.config
	provider.mu.Unlock()

	poolConfig, err := pgxpool.ParseConfig(string(config.URL.Bytes()))
	if err != nil {
		provider.markStartFailed()
		return db.ErrBackend
	}
	poolConfig.MaxConns = config.MaxConnections
	poolConfig.MinConns = config.MinConnections
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		provider.markStartFailed()
		return db.ErrBackend
	}
	healthContext, cancel := context.WithTimeout(ctx, config.HealthTimeout)
	err = pool.Ping(healthContext)
	cancel()
	if err != nil {
		pool.Close()
		provider.markStartFailed()
		return normalizeError(err, false)
	}

	provider.mu.Lock()
	provider.pool = pool
	provider.start = false
	provider.mu.Unlock()
	return nil
}

func (provider *Database) markStartFailed() {
	provider.mu.Lock()
	provider.start = false
	provider.mu.Unlock()
}

// Stop closes the shared pool and is safe after a failed or partial start.
func (provider *Database) Stop(_ context.Context) error {
	if provider == nil {
		return nil
	}
	provider.mu.Lock()
	if provider.start {
		provider.mu.Unlock()
		return ErrState
	}
	pool := provider.pool
	provider.pool = nil
	provider.mu.Unlock()
	if pool != nil {
		pool.Close()
	}
	return nil
}

// RetryCleanup is the construction-cleanup hook used by generated assembly.
func (provider *Database) RetryCleanup(ctx context.Context) error { return provider.Stop(ctx) }

// Health pings the already-started shared pool.
func (provider *Database) Health(ctx context.Context) error {
	pool, err := provider.currentPool()
	if err != nil {
		return err
	}
	if ctx == nil {
		return fmt.Errorf("%w: %w", db.ErrBackend, context.Canceled)
	}
	return normalizeError(pool.Ping(ctx), false)
}

// Exec executes one validated plan in a SERIALIZABLE transaction.
func (provider *Database) Exec(ctx context.Context, plan db.Plan) (db.Result, error) {
	if ctx == nil {
		return db.Result{}, fmt.Errorf("%w: %w", db.ErrBackend, context.Canceled)
	}
	if err := plan.Validate(); err != nil {
		return db.Result{}, err
	}
	pool, err := provider.currentPool()
	if err != nil {
		return db.Result{}, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return db.Result{}, normalizeError(err, false)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	result := db.Result{Statements: make([]db.StatementResult, 0, len(plan.Statements))}
	for _, statement := range plan.Statements {
		if statement.ReturnsRows {
			statementResult, err := queryStatement(ctx, tx, statement)
			if err != nil {
				return db.Result{}, normalizeError(err, false)
			}
			result.Statements = append(result.Statements, statementResult)
			continue
		}
		tag, err := tx.Exec(ctx, statement.SQL, statement.Args...)
		if err != nil {
			return db.Result{}, normalizeError(err, false)
		}
		count := uint64(tag.RowsAffected())
		if !statement.Affected.Accepts(count) {
			return db.Result{}, fmt.Errorf("%w: affected rows", db.ErrBackend)
		}
		result.Statements = append(result.Statements, db.StatementResult{AffectedRows: count})
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Result{}, normalizeError(err, true)
	}
	return result, nil
}

func queryStatement(ctx context.Context, tx pgx.Tx, statement db.Statement) (db.StatementResult, error) {
	rows, err := tx.Query(ctx, statement.SQL, statement.Args...)
	if err != nil {
		return db.StatementResult{}, err
	}
	defer rows.Close()
	var values [][]any
	for rows.Next() {
		row, err := rows.Values()
		if err != nil {
			return db.StatementResult{}, err
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		return db.StatementResult{}, err
	}
	valid := len(values) > 0
	switch statement.Cardinality {
	case db.CardinalityExactlyOne:
		valid = len(values) == 1
	case db.CardinalityZeroOrOne:
		valid = len(values) <= 1
	case db.CardinalityMany:
		valid = true
	default:
		valid = false
	}
	if !valid {
		return db.StatementResult{}, fmt.Errorf("%w: row cardinality", db.ErrBackend)
	}
	return db.StatementResult{Rows: values}, nil
}

func (provider *Database) currentPool() (*pgxpool.Pool, error) {
	if provider == nil {
		return nil, db.ErrUnavailable
	}
	provider.mu.RLock()
	pool := provider.pool
	provider.mu.RUnlock()
	if pool == nil {
		return nil, db.ErrUnavailable
	}
	return pool, nil
}

func normalizeError(err error, commit bool) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		if commit {
			return db.ErrResultUnknown
		}
		return fmt.Errorf("%w: %w", db.ErrBackend, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		if commit {
			return db.ErrResultUnknown
		}
		return fmt.Errorf("%w: %w", db.ErrTimeout, context.DeadlineExceeded)
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23505", "23P01":
			return db.ErrConflict
		case "23503":
			return db.ErrForeignKey
		case "40001", "40P01":
			return db.ErrConcurrent
		case "53300", "57P03":
			return db.ErrUnavailable
		}
	}
	if commit {
		return db.ErrResultUnknown
	}
	return db.ErrBackend
}
