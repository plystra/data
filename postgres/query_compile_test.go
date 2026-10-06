package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/plystra/data/declaration"
	"github.com/plystra/data/postgres"
)

func TestCompileQueriesIsDeterministicAndLowersSelectIntent(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1",
		Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables: []declaration.Table{{
				ID: "users",
				Columns: []declaration.Column{
					{ID: "id", Type: declaration.TypeInt64},
					{ID: "email", Type: declaration.TypeString},
					{ID: "active", Type: declaration.TypeBool},
				},
			}},
			Queries: []declaration.Query{{
				ID:        "list-active",
				Operation: declaration.OperationSelect,
				Table:     declaration.Ref{Symbol: "users"},
				Projection: []declaration.Projection{
					{Column: "id"},
					{Column: "email", Alias: "address"},
				},
				Predicate: declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{
					{Kind: declaration.PredicateEqual, Left: "active", Value: declaration.Bool(true)},
					{Kind: declaration.PredicateGreater, Left: "id", Value: declaration.Int64(10)},
				}},
				Ordering: []declaration.Ordering{{Column: "email", Direction: declaration.OrderingAscending}},
				Limit:    25, HasLimit: true, Offset: 5, HasOffset: true,
			}},
		},
	}
	first, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	second, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Path != "queries/accounts.user/v1/list-active.sql" || first[0].SQL != second[0].SQL || first[0].Digest != second[0].Digest || first[0].MemberID != member.MemberID {
		t.Fatalf("artifacts differ: first=%#v second=%#v", first, second)
	}
	for _, want := range []string{
		"-- plystra.data.postgres-query/v1 accounts.user/v1#list-active",
		`SELECT "id", "email" AS "address" FROM "accounts"."users"`,
		`WHERE ("active" = TRUE) AND ("id" > 10)`,
		`ORDER BY "email" ASC LIMIT 25 OFFSET 5;`,
	} {
		if !strings.Contains(first[0].SQL, want) {
			t.Fatalf("SQL missing %q:\n%s", want, first[0].SQL)
		}
	}
}

func TestCompileQueriesRejectsInvalidAndUnsupportedIntent(t *testing.T) {
	base := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}},
		},
	}
	tests := []struct {
		name  string
		query declaration.Query
		want  string
	}{
		{name: "mutation", query: declaration.Query{ID: "update", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "users"}}, want: "typed mutation inputs"},
		{name: "unknown projection", query: declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Projection: []declaration.Projection{{Column: "missing"}}}, want: "projection column missing"},
		{name: "unknown predicate", query: declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Predicate: declaration.Predicate{Kind: declaration.PredicateIsNull, Left: "missing"}}, want: "predicate column missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base.Model.Queries = []declaration.Query{test.query}
			_, err := postgres.CompileQueries([]postgres.SchemaMember{base})
			if err == nil || !errors.Is(err, postgres.ErrQueryCompilation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompileQueriesRejectsCrossResourceTable(t *testing.T) {
	left := postgres.SchemaMember{
		MemberID: "left.app/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "left", Imports: []string{"right.app/v1"},
			Tables:  []declaration.Table{{ID: "items", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}},
			Queries: []declaration.Query{{ID: "read-right", Operation: declaration.OperationSelect, Table: declaration.Ref{Member: "right.app/v1", Symbol: "items"}}},
		},
	}
	right := postgres.SchemaMember{
		MemberID: "right.app/v1", Resource: "database.secondary",
		Model: declaration.Model{Namespace: "right", Tables: []declaration.Table{{ID: "items", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}}},
	}
	_, err := postgres.CompileQueries([]postgres.SchemaMember{left, right})
	if !errors.Is(err, postgres.ErrCrossResourceReference) {
		t.Fatalf("error = %v, want cross-Resource reference", err)
	}
}
