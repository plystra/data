//go:build integration

package postgres_test

import (
	"errors"
	"testing"

	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/postgres/compiler"
)

func TestPostgresCompiledPlanInputsAndReturningAreAtomic(t *testing.T) {
	ctx, provider, conn := migrationDatabase(t)
	member := compiler.SchemaMember{MemberID: "registration.records/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "registration",
		Tables: []declaration.Table{
			{ID: "parents", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "label", Type: declaration.TypeString}}, Constraints: []declaration.Constraint{{ID: "pk-parents", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}},
			{ID: "children", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "parent-id", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "pk-children", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}},
		},
		Queries: []declaration.Query{
			{ID: "parent", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "parents"}, Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}, {ID: "label", Type: declaration.TypeString}},
				Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}, {Column: "label", Value: declaration.Value{Set: true, Kind: declaration.TypeString, Parameter: "label"}}},
				Returning:   []declaration.Projection{{Column: "label"}, {Column: "id", Alias: "identifier"}}, Results: []declaration.ResultField{{ID: "id", Source: "identifier", Type: declaration.TypeInt64}, {ID: "label", Source: "label", Type: declaration.TypeString}}},
			{ID: "child", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "children"}, Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}, {ID: "parent", Type: declaration.TypeInt64}},
				Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}, {Column: "parent-id", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "parent"}}}},
		},
		Plans: []declaration.Plan{{ID: "register", Parameters: []declaration.Parameter{{ID: "parent-id", Type: declaration.TypeInt64}, {ID: "child-id", Type: declaration.TypeInt64}, {ID: "label", Type: declaration.TypeString}}, Statements: []declaration.PlanStatement{
			{ID: "parent", Query: declaration.Ref{Symbol: "parent"}, Cardinality: declaration.CardinalityExactlyOne, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromParameter: "parent-id"}, {Parameter: "label", FromParameter: "label"}}},
			{ID: "child", Query: declaration.Ref{Symbol: "child"}, Cardinality: declaration.CardinalityNone, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromParameter: "child-id"}, {Parameter: "parent", FromStatement: "parent", FromResult: "id"}}},
		}}},
	}}
	schema, err := compiler.CompileSchema([]compiler.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, schema.SQL); err != nil {
		t.Fatal(err)
	}
	execute := func(parent, child int64) (database.Result, error) {
		t.Helper()
		artifacts, err := compiler.CompilePlans([]compiler.SchemaMember{member})
		if err != nil {
			t.Fatal(err)
		}
		plan := artifacts[0].Plan
		plan.Inputs = map[string]database.Argument{"parent-id": database.Int64Argument(parent), "child-id": database.Int64Argument(child), "label": database.StringArgument("' OR true; --")}
		return provider.Exec(ctx, plan)
	}
	result, err := execute(1, 10)
	if err != nil || len(result.Statements) != 2 || result.Statements[0].Rows[0][1].Int64 != 1 {
		t.Fatalf("compiled plan did not commit: %v", err)
	}
	var parentID int64
	if err := conn.QueryRow(ctx, `SELECT "parent-id" FROM "registration"."children" WHERE id=10`).Scan(&parentID); err != nil || parentID != 1 {
		t.Fatal("RETURNING reference did not retain its typed column", err)
	}
	result, err = execute(2, 10)
	if !errors.Is(err, database.ErrConflict) || len(result.Statements) != 0 {
		t.Fatal("later unique conflict did not reject the complete compiled plan", err)
	}
	member.Model.Plans[0].Statements[1].Affected = declaration.AffectedRows{Set: true, Min: 2, Max: 2}
	result, err = execute(3, 30)
	if !errors.Is(err, database.ErrBackend) || len(result.Statements) != 0 {
		t.Fatal("affected-row failure did not reject the complete compiled plan", err)
	}
	var count int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM "registration"."parents"`).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed compiled plans left partial state", err)
	}
}
