package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/plystra/data/database"
	"github.com/plystra/data/postgres"
)

func TestExecRejectsInvalidInputsBeforePoolAcquisition(t *testing.T) {
	var provider *postgres.Database
	for _, statement := range []database.Statement{
		{ID: "read", SQL: "SELECT 1", ReturnsRows: true, Cardinality: "unknown"},
		{ID: "read", SQL: "SELECT $1", ReturnsRows: true, Cardinality: database.CardinalityMany, Args: []database.Argument{{Kind: "unknown"}}},
		{ID: "write", SQL: "INSERT INTO records VALUES ($1)", Affected: database.ExactAffectedRows(1), Args: []database.Argument{{}}},
	} {
		result, err := provider.Exec(context.Background(), database.Plan{Statements: []database.Statement{statement}})
		if !errors.Is(err, database.ErrInvalidPlan) || len(result.Statements) != 0 {
			t.Fatalf("Exec = %#v, %v; want invalid plan before unavailable pool", result, err)
		}
	}
	_, err := provider.Exec(context.Background(), database.Plan{Statements: []database.Statement{{ID: "read", SQL: "SELECT $1", ReturnsRows: true, Cardinality: database.CardinalityMany, Args: []database.Argument{database.NullArgument()}}}})
	if !errors.Is(err, database.ErrUnavailable) {
		t.Fatalf("explicit NULL rejected: %v", err)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []postgres.Config{
		{},
		{MaxConnections: 0, HealthTimeout: time.Second},
		{MaxConnections: 4, MinConnections: 5, HealthTimeout: time.Second},
	}
	for _, config := range tests {
		if value, err := postgres.New(config); value != nil || err == nil {
			t.Fatalf("New(%#v) = (%v, %v), want invalid configuration", config, value, err)
		}
	}
}
