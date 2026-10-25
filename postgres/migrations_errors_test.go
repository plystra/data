package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	db "github.com/plystra/data/database"
)

func TestReadMigrationHistoryRowsRejectsDisappearedTable(t *testing.T) {
	history, err := readMigrationHistoryRows(context.Background(), nil, &pgconn.PgError{
		Code:    "42P01",
		Message: "relation plystra_data_migrations does not exist",
	})
	if !errors.Is(err, db.ErrBackend) {
		t.Fatalf("readMigrationHistoryRows() error = %v, want ErrBackend", err)
	}
	if len(history) != 0 {
		t.Fatalf("history length = %d, want 0", len(history))
	}
}

func TestReadMigrationHistoryRowsNormalizesQueryAndRowsErrors(t *testing.T) {
	tests := []struct {
		name      string
		queryErr  error
		scanErr   error
		rowsErr   error
		want      error
		wantClose bool
	}{
		{name: "query resource exhaustion", queryErr: postgresError("53300"), want: db.ErrResourceExhausted},
		{name: "query timeout", queryErr: context.DeadlineExceeded, want: db.ErrTimeout},
		{name: "scan backend failure", scanErr: errors.New("private driver scan failure"), want: db.ErrBackend, wantClose: true},
		{name: "rows concurrent failure", rowsErr: postgresError("40001"), want: db.ErrConcurrent, wantClose: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := &fakeMigrationRows{scanErr: test.scanErr, rowsErr: test.rowsErr}
			history, err := readMigrationHistoryRows(context.Background(), rows, test.queryErr)
			if !errors.Is(err, test.want) {
				t.Fatalf("readMigrationHistoryRows() error = %v, want %v", err, test.want)
			}
			if history != nil {
				t.Fatalf("history = %#v, want nil on error", history)
			}
			if rows.closed != test.wantClose {
				t.Fatalf("rows.closed = %t, want %t", rows.closed, test.wantClose)
			}
			if strings.Contains(err.Error(), "private driver") {
				t.Fatalf("readMigrationHistoryRows() leaked driver details: %v", err)
			}
		})
	}
}

type fakeMigrationRows struct {
	scanErr error
	rowsErr error
	closed  bool
}

func (rows *fakeMigrationRows) Next() bool { return rows.scanErr != nil }

func (rows *fakeMigrationRows) Scan(...any) error {
	err := rows.scanErr
	rows.scanErr = nil
	return err
}

func (rows *fakeMigrationRows) Err() error { return rows.rowsErr }

func (rows *fakeMigrationRows) Close() { rows.closed = true }
