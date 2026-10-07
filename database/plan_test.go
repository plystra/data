package database_test

import (
	"errors"
	"testing"

	"github.com/plystra/data/database"
)

func TestPlanValidateRequiresClosedAssertions(t *testing.T) {
	tests := []struct {
		name string
		plan database.Plan
		want error
	}{
		{name: "empty", plan: database.Plan{}},
		{name: "query without cardinality", plan: database.Plan{Statements: []database.Statement{{ID: "read", SQL: "SELECT 1", ReturnsRows: true}}}},
		{name: "query with null result kind", plan: database.Plan{Statements: []database.Statement{{ID: "read", SQL: "SELECT 1", ReturnsRows: true, Cardinality: database.CardinalityMany, ResultColumns: []database.ResultColumn{{Kind: database.ValueNull}}}}}},
		{name: "mutation without affected rows", plan: database.Plan{Statements: []database.Statement{{ID: "write", SQL: "UPDATE records SET value = $1", Affected: database.AffectedRows{}}}}},
		{name: "mutation with result columns", plan: database.Plan{Statements: []database.Statement{{ID: "write", SQL: "UPDATE records SET value = $1", Affected: database.ExactAffectedRows(1), ResultColumns: []database.ResultColumn{{Kind: database.ValueInt64}}}}}},
		{name: "duplicate ids", plan: database.Plan{Statements: []database.Statement{{ID: "write", SQL: "DELETE FROM records", Affected: database.ExactAffectedRows(1)}, {ID: "write", SQL: "DELETE FROM records", Affected: database.ExactAffectedRows(1)}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.plan.Validate(); !errors.Is(err, database.ErrInvalidPlan) {
				t.Fatalf("Validate() error = %v, want ErrInvalidPlan", err)
			}
		})
	}
}

func TestPlanValidateAcceptsQueryAndMutation(t *testing.T) {
	plan := database.Plan{Statements: []database.Statement{
		{ID: "create", SQL: "INSERT INTO records (value) VALUES ($1)", Affected: database.ExactAffectedRows(1)},
		{ID: "read", SQL: "SELECT id, value FROM records", ReturnsRows: true, Cardinality: database.CardinalityMany},
	}}
	if err := plan.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !database.ExactAffectedRows(2).Accepts(2) || database.ExactAffectedRows(2).Accepts(1) {
		t.Fatal("exact affected-row assertion is incorrect")
	}
	if !database.AtLeastAffectedRows(2).Accepts(9) || database.AtLeastAffectedRows(2).Accepts(1) {
		t.Fatal("unbounded affected-row assertion is incorrect")
	}
}
