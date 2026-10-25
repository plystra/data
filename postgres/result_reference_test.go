package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/plystra/data/database"
	"github.com/plystra/data/postgres"
)

func TestExecRejectsResultBindingsBeforePoolAcquisition(t *testing.T) {
	var provider *postgres.Database
	plan := database.Plan{Statements: []database.Statement{{
		ID: "write", SQL: "INSERT INTO records VALUES ($1)", Affected: database.ExactAffectedRows(1),
		Args: []database.Argument{database.ResultArgument(database.ValueInt64, "absent", 0)},
	}}}
	result, err := provider.Exec(context.Background(), plan)
	if !errors.Is(err, database.ErrInvalidPlan) || len(result.Statements) != 0 {
		t.Fatalf("invalid binding reached pool acquisition: %v", err)
	}
}

func TestExecRejectsUnboundInputsBeforePoolAcquisition(t *testing.T) {
	var provider *postgres.Database
	plan := database.Plan{Statements: []database.Statement{{
		ID: "write", SQL: "INSERT INTO records VALUES ($1)", Affected: database.ExactAffectedRows(1),
		Args: []database.Argument{database.InputArgument(database.ValueString, "secret")},
	}}}
	result, err := provider.Exec(context.Background(), plan)
	if !errors.Is(err, database.ErrInvalidPlan) || len(result.Statements) != 0 {
		t.Fatal("unbound input reached pool acquisition")
	}
}
