package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	db "github.com/plystra/data/database"
)

func TestNormalizeErrorClassifiesBoundedBackendErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		commit bool
		want   error
	}{
		{name: "no rows", err: pgx.ErrNoRows, want: db.ErrNotFound},
		{name: "unique conflict", err: postgresError("23505"), want: db.ErrConflict},
		{name: "exclusion conflict", err: postgresError("23P01"), want: db.ErrConflict},
		{name: "foreign key", err: postgresError("23503"), want: db.ErrForeignKey},
		{name: "serialization", err: postgresError("40001"), want: db.ErrConcurrent},
		{name: "deadlock", err: postgresError("40P01"), want: db.ErrConcurrent},
		{name: "lock unavailable", err: postgresError("55P03"), want: db.ErrConcurrent},
		{name: "server timeout", err: postgresError("57014"), want: db.ErrTimeout},
		{name: "resource class", err: postgresError("53000"), want: db.ErrResourceExhausted},
		{name: "too many connections", err: postgresError("53300"), want: db.ErrResourceExhausted},
		{name: "cannot connect now", err: postgresError("57P03"), want: db.ErrUnavailable},
		{name: "connection failure", err: postgresError("08006"), want: db.ErrUnavailable},
		{name: "statement completion unknown", err: postgresError("40003"), want: db.ErrResultUnknown},
		{name: "unknown backend", err: errors.New("private driver failure"), want: db.ErrBackend},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeError(test.err, test.commit)
			if !errors.Is(got, test.want) {
				t.Fatalf("normalizeError(%v) = %v, want %v", test.err, got, test.want)
			}
			assertNoBackendDetails(t, got)
		})
	}
}

func TestNormalizeErrorPreservesContextAndCommitUncertainty(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		commit        bool
		want          error
		wantCause     error
		wantCauseText bool
	}{
		{name: "cancelled before commit", err: context.Canceled, want: db.ErrBackend, wantCause: context.Canceled, wantCauseText: true},
		{name: "deadline before commit", err: context.DeadlineExceeded, want: db.ErrTimeout, wantCause: context.DeadlineExceeded, wantCauseText: true},
		{name: "cancelled during commit", err: context.Canceled, commit: true, want: db.ErrResultUnknown, wantCause: context.Canceled, wantCauseText: true},
		{name: "deadline during commit", err: context.DeadlineExceeded, commit: true, want: db.ErrResultUnknown, wantCause: context.DeadlineExceeded, wantCauseText: true},
		{name: "driver failure during commit", err: errors.New("private driver acknowledgement failure"), commit: true, want: db.ErrResultUnknown, wantCauseText: false},
		{name: "connection loss during commit", err: postgresError("08006"), commit: true, want: db.ErrResultUnknown, wantCauseText: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := normalizeError(test.err, test.commit)
			if !errors.Is(got, test.want) {
				t.Fatalf("normalizeError(%v, commit=%t) = %v, want %v", test.err, test.commit, got, test.want)
			}
			if test.wantCause != nil && !errors.Is(got, test.wantCause) {
				t.Fatalf("normalized error %v does not preserve %v", got, test.wantCause)
			}
			if !test.wantCauseText && strings.Contains(got.Error(), "private driver") {
				t.Fatalf("normalized error leaked driver details: %v", got)
			}
			assertNoBackendDetails(t, got)
		})
	}
}

func TestNormalizeErrorDoesNotExposePostgresDetails(t *testing.T) {
	err := normalizeError(&pgconn.PgError{
		Code:    "XX000",
		Message: "secret SQL: SELECT password FROM users",
		Detail:  "dsn=postgres://user:secret@example.test/db",
	}, false)
	if !errors.Is(err, db.ErrBackend) {
		t.Fatalf("normalized error = %v, want ErrBackend", err)
	}
	assertNoBackendDetails(t, err)
}

func TestValidateRowCardinality(t *testing.T) {
	tests := []struct {
		name        string
		cardinality db.Cardinality
		count       int
		want        error
	}{
		{name: "exactly one zero rows", cardinality: db.CardinalityExactlyOne, want: db.ErrNotFound},
		{name: "exactly one row", cardinality: db.CardinalityExactlyOne, count: 1},
		{name: "exactly one too many", cardinality: db.CardinalityExactlyOne, count: 2, want: db.ErrBackend},
		{name: "zero or one zero rows", cardinality: db.CardinalityZeroOrOne},
		{name: "zero or one one row", cardinality: db.CardinalityZeroOrOne, count: 1},
		{name: "zero or one too many", cardinality: db.CardinalityZeroOrOne, count: 2, want: db.ErrBackend},
		{name: "many zero rows", cardinality: db.CardinalityMany},
		{name: "many rows", cardinality: db.CardinalityMany, count: 2},
		{name: "invalid cardinality", cardinality: db.Cardinality("invalid"), count: 1, want: db.ErrBackend},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRowCardinality(test.cardinality, test.count)
			if test.want == nil {
				if err != nil {
					t.Fatalf("validateRowCardinality() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("validateRowCardinality() = %v, want %v", err, test.want)
			}
		})
	}
}

func postgresError(code string) error {
	return &pgconn.PgError{
		Code:    code,
		Message: "private driver message",
		Detail:  "private SQL and DSN details",
	}
}

func assertNoBackendDetails(t *testing.T, err error) {
	t.Helper()
	for _, fragment := range []string{"private driver", "secret SQL", "postgres://", "DSN details"} {
		if strings.Contains(err.Error(), fragment) {
			t.Fatalf("normalized error %q contains backend detail %q", err, fragment)
		}
	}
}
