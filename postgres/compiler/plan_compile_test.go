package compiler_test

import (
	"errors"
	"testing"

	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
	postgres "github.com/plystra/data/postgres/compiler"
)

func TestCompilePlansBuildsDeterministicTypedSelectPlan(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}}}},
			Queries: []declaration.Query{{
				ID: "read-user", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
				Projection: []declaration.Projection{{Column: "id"}, {Column: "email", Alias: "address"}},
				Results:    []declaration.ResultField{{ID: "email", Source: "address", Type: declaration.TypeString}, {ID: "id", Source: "id", Type: declaration.TypeInt64}},
			}},
			Plans: []declaration.Plan{{ID: "read-plan", Statements: []declaration.PlanStatement{{
				ID: "read", Query: declaration.Ref{Symbol: "read-user"}, Cardinality: declaration.CardinalityExactlyOne,
			}}}},
		},
	}
	first, err := postgres.CompilePlans([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	second, err := postgres.CompilePlans([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].Path != "plans/accounts.user/v1/read-plan.json" || first[0].Digest != second[0].Digest {
		t.Fatalf("plan artifacts = %#v and %#v", first, second)
	}
	plan := first[0].Plan
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(plan.Statements) != 1 {
		t.Fatalf("statements = %#v", plan.Statements)
	}
	statement := plan.Statements[0]
	if !statement.ReturnsRows || statement.Cardinality != database.CardinalityExactlyOne || len(statement.ResultColumns) != 2 || statement.ResultColumns[0].Kind != database.ValueInt64 || statement.ResultColumns[1].Kind != database.ValueString || statement.ResultColumns[1].Nullable {
		t.Fatalf("compiled statement = %#v", statement)
	}
	if statement.SQL == "" {
		t.Fatal("compiled statement has no SQL")
	}
}

func TestCompilePlansRejectsUnsupportedParameterAndMutationShapes(t *testing.T) {
	base := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "name", Type: declaration.TypeString}}}},
		},
	}
	base.Model.Queries = []declaration.Query{{
		ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
		Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
		Projection: []declaration.Projection{{Column: "id"}},
		Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}}
	base.Model.Plans = []declaration.Plan{{ID: "read-plan", Statements: []declaration.PlanStatement{{ID: "read", Query: declaration.Ref{Symbol: "read"}, Cardinality: declaration.CardinalityMany}}}}
	if _, err := postgres.CompilePlans([]postgres.SchemaMember{base}); !errors.Is(err, postgres.ErrPlanCompilation) {
		t.Fatalf("parameterized plan error = %v", err)
	}
	base.Model.Queries[0] = declaration.Query{ID: "write", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"}, Assignments: []declaration.Assignment{{Column: "name", Value: declaration.String("Ada")}}}
	base.Model.Plans[0].Statements[0] = declaration.PlanStatement{ID: "write", Query: declaration.Ref{Symbol: "write"}, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}}
	if _, err := postgres.CompilePlans([]postgres.SchemaMember{base}); !errors.Is(err, postgres.ErrPlanCompilation) {
		t.Fatalf("mutation plan error = %v", err)
	}
}
