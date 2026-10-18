package compiler_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/plystra/data/declaration"
	postgres "github.com/plystra/data/postgres/compiler"
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
				Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}, {ID: "email", Source: "address", Type: declaration.TypeString}},
				Predicate: declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{
					{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "active"}, Value: declaration.Bool(true)},
					{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(10)},
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
	want := "-- plystra.data.postgres-query/v1 accounts.user/v1#list-active\n" +
		`SELECT "id", "email" AS "address" FROM "accounts"."users" WHERE ("active" = TRUE) AND ("id" > 10) ORDER BY "email" ASC LIMIT 25 OFFSET 5;` + "\n"
	if first[0].SQL != want {
		t.Fatalf("single-table SQL = %q, want %q", first[0].SQL, want)
	}
}

func TestCompileQueriesLowersCountProjection(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}},
			Queries: []declaration.Query{
				{
					ID: "count-users", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
					Projection: []declaration.Projection{{Aggregate: declaration.AggregateCount, Alias: "total"}},
					Results:    []declaration.ResultField{{ID: "total", Source: "total", Type: declaration.TypeInt64}},
				},
				{
					ID: "count-user-ids", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
					Projection: []declaration.Projection{{Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}},
					Results:    []declaration.ResultField{{ID: "total", Source: "total", Type: declaration.TypeInt64}},
				},
			},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("count artifacts = %#v", artifacts)
	}
	want := map[string]string{
		"count-users":    `SELECT COUNT(*) AS "total" FROM "accounts"."users";`,
		"count-user-ids": `SELECT COUNT("id") AS "total" FROM "accounts"."users";`,
	}
	for _, artifact := range artifacts {
		name := strings.TrimSuffix(strings.TrimPrefix(artifact.Path, "queries/accounts.user/v1/"), ".sql")
		if !strings.Contains(artifact.SQL, want[name]) {
			t.Fatalf("count SQL = %#v, want %q", artifact, want[name])
		}
	}
}

func TestCompileQueriesLowersMinProjection(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "score", Type: declaration.TypeInt64}}}},
			Queries: []declaration.Query{{
				ID: "minimum-score", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
				Projection: []declaration.Projection{{Aggregate: declaration.AggregateMin, Column: "score", Alias: "minimum"}},
				Results:    []declaration.ResultField{{ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}},
			}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("min artifacts = %#v, error = %v", artifacts, err)
	}
	want := `SELECT MIN("score") AS "minimum" FROM "accounts"."users";`
	if !strings.Contains(artifacts[0].SQL, want) || !artifacts[0].Results[0].Nullable {
		t.Fatalf("min artifact = %#v, want %q", artifacts[0], want)
	}
}

func TestCompileQueriesLowersMaxProjection(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "score", Type: declaration.TypeInt64}}}},
			Queries: []declaration.Query{{
				ID: "maximum-score", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
				Projection: []declaration.Projection{{Aggregate: declaration.AggregateMax, Column: "score", Alias: "maximum"}},
				Results:    []declaration.ResultField{{ID: "maximum", Source: "maximum", Type: declaration.TypeInt64, Nullable: true}},
			}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("max artifacts = %#v, error = %v", artifacts, err)
	}
	want := `SELECT MAX("score") AS "maximum" FROM "accounts"."users";`
	if !strings.Contains(artifacts[0].SQL, want) || !artifacts[0].Results[0].Nullable {
		t.Fatalf("max artifact = %#v, want %q", artifacts[0], want)
	}
}

func TestCompileQueriesLowersGroupedAggregate(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "region", Type: declaration.TypeString}, {ID: "tier", Type: declaration.TypeString}, {ID: "id", Type: declaration.TypeInt64}}}},
			Queries: []declaration.Query{{
				ID: "count-by-region", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
				GroupBy:    []declaration.ColumnRef{{Column: "region"}, {Column: "tier"}},
				Projection: []declaration.Projection{{Column: "region"}, {Column: "tier"}, {Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}},
				Having:     declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Int64(1)},
				Results:    []declaration.ResultField{{ID: "region", Source: "region", Type: declaration.TypeString}, {ID: "tier", Source: "tier", Type: declaration.TypeString}, {ID: "total", Source: "total", Type: declaration.TypeInt64}},
			}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("grouped artifacts = %#v, error = %v", artifacts, err)
	}
	want := `SELECT "region", "tier", COUNT("id") AS "total" FROM "accounts"."users" GROUP BY "region", "tier" HAVING COUNT("id") > 1;`
	if !strings.Contains(artifacts[0].SQL, want) {
		t.Fatalf("grouped artifact = %#v, want %q", artifacts[0], want)
	}
	member.Model.Queries[0].Having = declaration.Predicate{}
	artifacts, err = postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("grouped artifact without HAVING = %#v, error = %v", artifacts, err)
	}
	want = `SELECT "region", "tier", COUNT("id") AS "total" FROM "accounts"."users" GROUP BY "region", "tier";`
	if !strings.Contains(artifacts[0].SQL, want) {
		t.Fatalf("grouped SQL without HAVING = %q, want %q", artifacts[0].SQL, want)
	}
	member.Model.Queries[0].Projection[0].Alias = "zone"
	member.Model.Queries[0].Results[0].Source = "zone"
	member.Model.Queries[0].Having = declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Int64(1)}
	artifacts, err = postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("aliased grouped artifacts = %#v, error = %v", artifacts, err)
	}
	want = `SELECT "region" AS "zone", "tier", COUNT("id") AS "total" FROM "accounts"."users" GROUP BY "region", "tier" HAVING COUNT("id") > 1;`
	if !strings.Contains(artifacts[0].SQL, want) {
		t.Fatalf("aliased grouped artifact = %#v, want %q", artifacts[0], want)
	}
	baselineDigest := artifacts[0].Digest
	member.Model.Queries[0].Ordering = []declaration.Ordering{{Column: "total", Direction: declaration.OrderingDescending}, {Column: "zone", Direction: declaration.OrderingAscending}}
	artifacts, err = postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("ordered grouped artifacts = %#v, error = %v", artifacts, err)
	}
	want = `SELECT "region" AS "zone", "tier", COUNT("id") AS "total" FROM "accounts"."users" GROUP BY "region", "tier" HAVING COUNT("id") > 1 ORDER BY COUNT("id") DESC, "users"."region" ASC;`
	if !strings.Contains(artifacts[0].SQL, want) || artifacts[0].Digest == baselineDigest {
		t.Fatalf("ordered grouped artifact = %#v, want %q and new digest", artifacts[0], want)
	}
	member.Model.Queries[0].Having = declaration.Predicate{}
	member.Model.Queries[0].Projection[2].Alias = "region"
	member.Model.Queries[0].Results[2].Source = "region"
	member.Model.Queries[0].Ordering = []declaration.Ordering{{Column: "zone", Direction: declaration.OrderingAscending}}
	artifacts, err = postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("alias-collision grouped artifacts = %#v, error = %v", artifacts, err)
	}
	want = `SELECT "region" AS "zone", "tier", COUNT("id") AS "region" FROM "accounts"."users" GROUP BY "region", "tier" ORDER BY "users"."region" ASC;`
	if !strings.Contains(artifacts[0].SQL, want) {
		t.Fatalf("alias-collision grouped artifact = %#v, want %q", artifacts[0], want)
	}
}

func TestCompileQueriesLowersTypedGroupedHaving(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{
				{ID: "region", Type: declaration.TypeString}, {ID: "score", Type: declaration.TypeInt64},
			}}},
			Queries: []declaration.Query{{
				ID: "minimum-by-region", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
				Parameters: []declaration.Parameter{{ID: "threshold", Type: declaration.TypeInt64}},
				GroupBy:    []declaration.ColumnRef{{Column: "region"}},
				Projection: []declaration.Projection{{Column: "region"}, {Aggregate: declaration.AggregateMin, Column: "score", Alias: "minimum"}},
				Having: declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{
					{Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "minimum"}},
					{Kind: declaration.PredicateGreaterEqual, Left: declaration.ColumnRef{Column: "minimum"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "threshold"}},
					{Kind: declaration.PredicateNotEqual, Left: declaration.ColumnRef{Column: "region"}, Value: declaration.String("inactive")},
				}},
				Ordering: []declaration.Ordering{{Column: "minimum", Direction: declaration.OrderingAscending}, {Column: "region", Direction: declaration.OrderingDescending}},
				Results:  []declaration.ResultField{{ID: "region", Source: "region", Type: declaration.TypeString}, {ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}},
			}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	want := `SELECT "region", MIN("score") AS "minimum" FROM "accounts"."users" GROUP BY "region" HAVING (MIN("score") IS NOT NULL) AND (MIN("score") >= $1) AND ("region" <> 'inactive') ORDER BY MIN("score") ASC, "users"."region" DESC;`
	if !strings.Contains(artifacts[0].SQL, want) {
		t.Fatalf("SQL = %q, want %q", artifacts[0].SQL, want)
	}
	member.Model.Queries[0].Parameters[0].Type = declaration.TypeString
	if _, err := postgres.CompileQueries([]postgres.SchemaMember{member}); err == nil || !errors.Is(err, postgres.ErrQueryCompilation) {
		t.Fatalf("mismatched HAVING parameter error = %v", err)
	}
}

func TestCompileQueriesCountsNullableJoinedColumn(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Symbol: "teams"}
	member := postgres.SchemaMember{MemberID: "accounts.user/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts",
		Tables: []declaration.Table{
			{ID: "users", Columns: []declaration.Column{{ID: "team-id", Type: declaration.TypeInt64}}},
			{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}},
		},
		Queries: []declaration.Query{{
			ID: "count-teams", Operation: declaration.OperationSelect, Table: users,
			Joins: []declaration.Join{{Kind: declaration.JoinLeft, Table: teams, On: declaration.JoinCondition{
				Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"},
			}}},
			Projection: []declaration.Projection{{Table: teams, Column: "id", Aggregate: declaration.AggregateCount, Alias: "total"}},
			Results:    []declaration.ResultField{{ID: "total", Source: "total", Type: declaration.TypeInt64}},
		}},
	}}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT COUNT("t1"."id") AS "total" FROM "accounts"."users" AS "t0" LEFT JOIN "accounts"."teams" AS "t1" ON "t0"."team-id" = "t1"."id";`
	if len(artifacts) != 1 || !strings.Contains(artifacts[0].SQL, want) || artifacts[0].Results[0].Nullable {
		t.Fatalf("joined count artifacts = %#v, want %q", artifacts, want)
	}
}

func TestCompileQueriesSupportsTypedMutationIntent(t *testing.T) {
	base := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{
				{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString},
			}}},
		},
	}
	base.Model.Queries = []declaration.Query{
		{
			ID: "insert", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"},
			Parameters:  []declaration.Parameter{{ID: "email", Type: declaration.TypeString}},
			Assignments: []declaration.Assignment{{Column: "email", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "email"}}},
		},
		{
			ID: "update", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "users"},
			Parameters:  []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
			Assignments: []declaration.Assignment{{Column: "email", Value: declaration.String("updated")}},
			Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
		},
		{
			ID: "delete", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "users"},
			Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
			Predicate:  declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{base})
	if err != nil || len(artifacts) != 3 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	joined := artifacts[0].SQL + artifacts[1].SQL + artifacts[2].SQL
	for _, want := range []string{
		`INSERT INTO "accounts"."users" ("email") VALUES ($1);`,
		`UPDATE "accounts"."users" SET "email" = 'updated' WHERE "id" = $1;`,
		`DELETE FROM "accounts"."users" WHERE "id" = $1;`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("SQL missing %q:\n%s", want, joined)
		}
	}
}

func TestCompileQueriesLowersTypedReturningAndResultMapping(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{
				{ID: "id", Type: declaration.TypeInt64},
				{ID: "email", Type: declaration.TypeString, Nullable: true},
			}}},
			Queries: []declaration.Query{
				{
					ID: "insert-user", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"},
					Parameters:  []declaration.Parameter{{ID: "email", Type: declaration.TypeString}},
					Assignments: []declaration.Assignment{{Column: "email", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "email"}}},
					Returning:   []declaration.Projection{{Column: "id", Alias: "user-id"}, {Column: "email"}},
					Results:     []declaration.ResultField{{ID: "id", Source: "user-id", Type: declaration.TypeInt64}, {ID: "email", Source: "email", Type: declaration.TypeString, Nullable: true}},
				},
				{
					ID: "update-user", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "users"},
					Assignments: []declaration.Assignment{{Column: "email", Value: declaration.String("updated")}},
					Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(1)},
					Returning:   []declaration.Projection{{Column: "email"}},
					Results:     []declaration.ResultField{{ID: "email", Source: "email", Type: declaration.TypeString, Nullable: true}},
				},
				{
					ID: "delete-user", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "users"},
					Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(1)},
					Returning: []declaration.Projection{{Column: "id"}},
					Results:   []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
				},
				{
					ID: "select-user", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
					Projection: []declaration.Projection{{Column: "id", Alias: "user-id"}},
					Results:    []declaration.ResultField{{ID: "id", Source: "user-id", Type: declaration.TypeInt64}},
				},
			},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 4 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	wants := map[string]string{
		"insert-user": `INSERT INTO "accounts"."users" ("email") VALUES ($1) RETURNING "id" AS "user-id", "email";`,
		"update-user": `UPDATE "accounts"."users" SET "email" = 'updated' WHERE "id" = 1 RETURNING "email";`,
		"delete-user": `DELETE FROM "accounts"."users" WHERE "id" = 1 RETURNING "id";`,
		"select-user": `SELECT "id" AS "user-id" FROM "accounts"."users";`,
	}
	for _, artifact := range artifacts {
		name := strings.TrimSuffix(strings.TrimPrefix(artifact.Path, "queries/accounts.user/v1/"), ".sql")
		if !strings.Contains(artifact.SQL, wants[name]) || len(artifact.Results) == 0 {
			t.Fatalf("artifact = %#v", artifact)
		}
	}
}

func TestCompileQueriesLowersNamedConstraintConflictIgnore(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "users-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}}},
			Queries:   []declaration.Query{{ID: "insert-user", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"}, Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Int64(1)}}, Conflict: declaration.Conflict{Constraint: "users-pk", Action: declaration.ConflictDoNothing}}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	want := `INSERT INTO "accounts"."users" ("id") VALUES (1) ON CONFLICT ON CONSTRAINT "users-pk" DO NOTHING;`
	if artifacts[0].SQL != "-- plystra.data.postgres-query/v1 accounts.user/v1#insert-user\n"+want+"\n" {
		t.Fatalf("SQL = %q, want %q", artifacts[0].SQL, want)
	}
}

func TestCompileQueriesLowersUniqueConstraintConflictIgnore(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "email", Type: declaration.TypeString}}, Constraints: []declaration.Constraint{{ID: "users-email-key", Kind: declaration.ConstraintUnique, Columns: []string{"email"}}}}},
			Queries:   []declaration.Query{{ID: "insert-user", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"}, Assignments: []declaration.Assignment{{Column: "email", Value: declaration.String("a@example.com")}}, Conflict: declaration.Conflict{Constraint: "users-email-key", Action: declaration.ConflictDoNothing}}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	want := "-- plystra.data.postgres-query/v1 accounts.user/v1#insert-user\nINSERT INTO \"accounts\".\"users\" (\"email\") VALUES ('a@example.com') ON CONFLICT ON CONSTRAINT \"users-email-key\" DO NOTHING;\n"
	if artifacts[0].SQL != want {
		t.Fatalf("SQL = %q, want %q", artifacts[0].SQL, want)
	}
}

func TestCompileQueriesLowersConflictUpdateWithTypedReturning(t *testing.T) {
	member := postgres.SchemaMember{MemberID: "accounts.user/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts",
		Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}, {ID: "name", Type: declaration.TypeString}}, Constraints: []declaration.Constraint{{ID: "users-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}}},
		Queries: []declaration.Query{{ID: "insert-user", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"},
			Parameters:  []declaration.Parameter{{ID: "name", Type: declaration.TypeString}, {ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}},
			Assignments: []declaration.Assignment{{Column: "name", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "name"}}, {Column: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}}, {Column: "email", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "email"}}},
			Conflict:    declaration.Conflict{Constraint: "users-pk", Action: declaration.ConflictDoUpdate, UpdateColumns: []string{"name", "email"}},
			Returning:   []declaration.Projection{{Column: "id", Alias: "user-id"}}, Results: []declaration.ResultField{{ID: "id", Source: "user-id", Type: declaration.TypeInt64}},
		}},
	}}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	want := "-- plystra.data.postgres-query/v1 accounts.user/v1#insert-user\nINSERT INTO \"accounts\".\"users\" (\"email\", \"id\", \"name\") VALUES ($1, $2, $3) ON CONFLICT ON CONSTRAINT \"users-pk\" DO UPDATE SET \"email\" = EXCLUDED.\"email\", \"name\" = EXCLUDED.\"name\" RETURNING \"id\" AS \"user-id\";\n"
	if artifacts[0].SQL != want {
		t.Fatalf("SQL = %q, want %q", artifacts[0].SQL, want)
	}
}

func TestCompileQueriesLowersImportedSameResourceConflictUpdate(t *testing.T) {
	owner := postgres.SchemaMember{MemberID: "accounts.user/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts", Imports: []string{"orders.audit/v1"},
		Queries: []declaration.Query{{ID: "insert-event", Operation: declaration.OperationInsert, Table: declaration.Ref{Member: "orders.audit/v1", Symbol: "events"},
			Assignments: []declaration.Assignment{{Column: "id", Value: declaration.String("event")}, {Column: "label", Value: declaration.String("new")}},
			Conflict:    declaration.Conflict{Constraint: "events-key", Action: declaration.ConflictDoUpdate, UpdateColumns: []string{"label"}},
		}},
	}}
	remote := postgres.SchemaMember{MemberID: "orders.audit/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "orders", Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeString}, {ID: "label", Type: declaration.TypeString}}, Constraints: []declaration.Constraint{{ID: "events-key", Kind: declaration.ConstraintUnique, Columns: []string{"id"}}}}},
	}}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{owner, remote})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	want := "-- plystra.data.postgres-query/v1 accounts.user/v1#insert-event\nINSERT INTO \"orders\".\"events\" (\"id\", \"label\") VALUES ('event', 'new') ON CONFLICT ON CONSTRAINT \"events-key\" DO UPDATE SET \"label\" = EXCLUDED.\"label\";\n"
	if artifacts[0].SQL != want {
		t.Fatalf("SQL = %q, want %q", artifacts[0].SQL, want)
	}
	remote.Resource = "database.other"
	if _, err := postgres.CompileQueries([]postgres.SchemaMember{owner, remote}); err == nil || !errors.Is(err, postgres.ErrQueryCompilation) {
		t.Fatalf("cross-Resource conflict update error = %v", err)
	}
}

func TestCompileQueriesLowersImportedSameResourceConflictIgnore(t *testing.T) {
	owner := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts", Imports: []string{"orders.audit/v1"},
			Queries: []declaration.Query{{
				ID: "insert-event", Operation: declaration.OperationInsert, Table: declaration.Ref{Member: "orders.audit/v1", Symbol: "events"},
				Assignments: []declaration.Assignment{{Column: "id", Value: declaration.String("event")}},
				Conflict:    declaration.Conflict{Constraint: "events-key", Action: declaration.ConflictDoNothing},
			}},
		},
	}
	remote := postgres.SchemaMember{
		MemberID: "orders.audit/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "orders",
			Tables:    []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeString}}, Constraints: []declaration.Constraint{{ID: "events-key", Kind: declaration.ConstraintUnique, Columns: []string{"id"}}}}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{owner, remote})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	want := "-- plystra.data.postgres-query/v1 accounts.user/v1#insert-event\nINSERT INTO \"orders\".\"events\" (\"id\") VALUES ('event') ON CONFLICT ON CONSTRAINT \"events-key\" DO NOTHING;\n"
	if artifacts[0].SQL != want {
		t.Fatalf("SQL = %q, want %q", artifacts[0].SQL, want)
	}
}

func TestCompileQueriesRejectsUnsupportedConflictTargets(t *testing.T) {
	base := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables: []declaration.Table{{
				ID:      "users",
				Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}},
				Constraints: []declaration.Constraint{
					{ID: "users-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}},
					{ID: "users-fk", Kind: declaration.ConstraintForeignKey, Columns: []string{"id"}, ReferencedTable: declaration.Ref{Symbol: "users"}, ReferencedCols: []string{"id"}},
				},
				Indexes: []declaration.Index{{ID: "users-unique-index", Columns: []string{"id"}, Unique: true}},
			}},
		},
	}
	for _, test := range []struct {
		name     string
		conflict declaration.Conflict
		want     string
	}{
		{name: "unknown constraint", conflict: declaration.Conflict{Constraint: "missing", Action: declaration.ConflictDoNothing}, want: "conflict constraint"},
		{name: "foreign key", conflict: declaration.Conflict{Constraint: "users-fk", Action: declaration.ConflictDoNothing}, want: "conflict constraint"},
		{name: "unique index", conflict: declaration.Conflict{Constraint: "users-unique-index", Action: declaration.ConflictDoNothing}, want: "conflict constraint"},
		{name: "empty constraint", conflict: declaration.Conflict{Action: declaration.ConflictDoNothing}, want: "query conflict"},
		{name: "missing update columns", conflict: declaration.Conflict{Constraint: "users-pk", Action: declaration.ConflictDoUpdate}, want: "query conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base.Model.Queries = []declaration.Query{{
				ID: "insert-user", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"},
				Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Int64(1)}}, Conflict: test.conflict,
			}}
			_, err := postgres.CompileQueries([]postgres.SchemaMember{base})
			if err == nil || !errors.Is(err, postgres.ErrQueryCompilation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompileQueriesRejectsInvalidTypedResultMapping(t *testing.T) {
	base := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{Namespace: "accounts", Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString, Nullable: true}}}}},
	}
	for _, test := range []struct {
		name    string
		results []declaration.ResultField
		want    string
	}{
		{"missing mapping", nil, "query result mapping"},
		{"wrong type", []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeString}}, "does not match"},
		{"wrong nullability", []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64, Nullable: true}}, "does not match"},
		{"unknown source", []declaration.ResultField{{ID: "id", Source: "missing", Type: declaration.TypeInt64}}, "query result source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base.Model.Queries = []declaration.Query{{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Projection: []declaration.Projection{{Column: "id"}}, Results: test.results}}
			_, err := postgres.CompileQueries([]postgres.SchemaMember{base})
			if !errors.Is(err, postgres.ErrQueryCompilation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompileQueriesRejectsInvalidIntent(t *testing.T) {
	base := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{Namespace: "accounts", Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}}},
	}
	for _, test := range []struct {
		name  string
		query declaration.Query
		want  string
	}{
		{name: "unknown projection", query: declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Projection: []declaration.Projection{{Column: "missing"}}, Results: []declaration.ResultField{{ID: "missing", Source: "missing", Type: declaration.TypeInt64}}}, want: "result column missing is not present"},
		{name: "unknown predicate", query: declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Predicate: declaration.Predicate{Kind: declaration.PredicateIsNull, Left: declaration.ColumnRef{Column: "missing"}}}, want: "predicate column missing"},
		{name: "predicate without kind", query: declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Predicate: declaration.Predicate{Left: declaration.ColumnRef{Column: "id"}}}, want: "predicate has fields without a kind"},
		{name: "predicate with ignored items", query: declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(1), Items: []declaration.Predicate{{Kind: declaration.PredicateIsNull, Left: declaration.ColumnRef{Column: "id"}}}}}, want: "predicate comparison has nested items"},
	} {
		t.Run(test.name, func(t *testing.T) {
			base.Model.Queries = []declaration.Query{test.query}
			artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{base})
			if err == nil || !errors.Is(err, postgres.ErrQueryCompilation) || !strings.Contains(err.Error(), test.want) || len(artifacts) != 0 {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompileQueriesLowersNotNullPredicate(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.user/v1", Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "email", Type: declaration.TypeString, Nullable: true}}}},
			Queries: []declaration.Query{{
				ID: "active-users", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
				Predicate: declaration.Predicate{Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "email"}},
			}},
		},
	}
	artifacts, err := postgres.CompileQueries([]postgres.SchemaMember{member})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
	}
	want := "-- plystra.data.postgres-query/v1 accounts.user/v1#active-users\nSELECT * FROM \"accounts\".\"users\" WHERE \"email\" IS NOT NULL;\n"
	if artifacts[0].SQL != want {
		t.Fatalf("SQL = %q, want %q", artifacts[0].SQL, want)
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

func TestCompileQueriesLowersQualifiedJoinsAndOwnerHeader(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Symbol: "teams"}
	regions := declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}
	owner := postgres.SchemaMember{MemberID: "accounts.user/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts", Imports: []string{"geo.region/v1"},
		Tables: []declaration.Table{
			{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "team-id", Type: declaration.TypeInt64}, {ID: "active", Type: declaration.TypeBool}}},
			{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "region-id", Type: declaration.TypeInt64}}},
		},
		Queries: []declaration.Query{
			{ID: "joined-users", Operation: declaration.OperationSelect, Table: users,
				Joins: []declaration.Join{
					{Kind: declaration.JoinInner, Table: teams, On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"}}},
					{Kind: declaration.JoinLeft, Table: regions, On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: teams, Column: "region-id"}, Right: declaration.ColumnRef{Table: regions, Column: "id"}}},
				},
				Projection: []declaration.Projection{{Table: users, Column: "id", Alias: "user-id"}, {Table: regions, Column: "id", Alias: "region-id"}},
				Results:    []declaration.ResultField{{ID: "id", Source: "user-id", Type: declaration.TypeInt64}, {ID: "region", Source: "region-id", Type: declaration.TypeInt64, Nullable: true}},
				Predicate: declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{
					{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "active"}, Value: declaration.Bool(true)},
					{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Table: teams, Column: "region-id"}, Right: declaration.ColumnRef{Table: regions, Column: "id"}},
				}},
				Ordering: []declaration.Ordering{
					{Column: "id", Direction: declaration.OrderingAscending},
					{Table: regions, Column: "id", Direction: declaration.OrderingDescending},
				},
			},
			{ID: "remote-regions", Operation: declaration.OperationSelect, Table: regions, Projection: []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}}},
		},
	}}
	remote := postgres.SchemaMember{MemberID: "geo.region/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "geo", Tables: []declaration.Table{{ID: "regions", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}},
	}}
	first, err := postgres.CompileQueries([]postgres.SchemaMember{remote, owner})
	if err != nil {
		t.Fatal(err)
	}
	second, err := postgres.CompileQueries([]postgres.SchemaMember{owner, remote})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || len(second) != 2 || first[0].SQL != second[0].SQL || first[0].Digest != second[0].Digest {
		t.Fatalf("unstable join artifacts: first=%#v second=%#v", first, second)
	}
	for _, artifact := range first {
		switch artifact.Path {
		case "queries/accounts.user/v1/joined-users.sql":
			want := "-- plystra.data.postgres-query/v1 accounts.user/v1#joined-users\n" +
				`SELECT "t0"."id" AS "user-id", "t2"."id" AS "region-id" FROM "accounts"."users" AS "t0" INNER JOIN "accounts"."teams" AS "t1" ON "t0"."team-id" = "t1"."id" LEFT JOIN "geo"."regions" AS "t2" ON "t1"."region-id" = "t2"."id" WHERE ("t0"."active" = TRUE) AND ("t1"."region-id" = "t2"."id") ORDER BY "t0"."id" ASC, "t2"."id" DESC;` + "\n"
			if artifact.SQL != want {
				t.Fatalf("joined SQL = %q, want %q", artifact.SQL, want)
			}
		case "queries/accounts.user/v1/remote-regions.sql":
			want := "-- plystra.data.postgres-query/v1 accounts.user/v1#remote-regions\n" + `SELECT "id" FROM "geo"."regions";` + "\n"
			if artifact.SQL != want {
				t.Fatalf("cross-member SQL = %q, want %q", artifact.SQL, want)
			}
		default:
			t.Fatalf("unexpected artifact %#v", artifact)
		}
	}
}

func TestCompileQueriesRejectsInvalidJoinedPredicateReferences(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Symbol: "teams"}
	base := postgres.SchemaMember{MemberID: "accounts.user/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts",
		Tables: []declaration.Table{
			{ID: "users", Columns: []declaration.Column{{ID: "team-id", Type: declaration.TypeInt64}}},
			{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "other-id", Type: declaration.TypeString}}},
		},
	}}
	for _, test := range []struct {
		name string
		edit func(*postgres.SchemaMember, *declaration.Predicate)
		want string
	}{
		{name: "missing table", edit: func(_ *postgres.SchemaMember, predicate *declaration.Predicate) {
			predicate.Left = declaration.ColumnRef{Table: declaration.Ref{Symbol: "missing"}, Column: "id"}
		}, want: `predicate table "missing"`},
		{name: "missing column", edit: func(_ *postgres.SchemaMember, predicate *declaration.Predicate) {
			predicate.Left = declaration.ColumnRef{Table: teams, Column: "missing"}
		}, want: "predicate column missing is not present"},
		{name: "mismatched columns", edit: func(member *postgres.SchemaMember, predicate *declaration.Predicate) {
			predicate.Value = declaration.Value{}
			predicate.Left = declaration.ColumnRef{Table: teams, Column: "other-id"}
			predicate.Right = declaration.ColumnRef{Table: users, Column: "team-id"}
		}, want: "predicate column types do not match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			member := base
			member.Model.Tables = append([]declaration.Table(nil), base.Model.Tables...)
			predicate := declaration.Predicate{Kind: declaration.PredicateEqual, Value: declaration.Int64(1)}
			test.edit(&member, &predicate)
			member.Model.Queries = []declaration.Query{{
				ID: "read", Operation: declaration.OperationSelect, Table: users,
				Joins: []declaration.Join{{Kind: declaration.JoinInner, Table: teams, On: declaration.JoinCondition{
					Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"},
				}}},
				Predicate: predicate,
			}}
			_, err := postgres.CompileQueries([]postgres.SchemaMember{member})
			if !errors.Is(err, postgres.ErrQueryCompilation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompileQueriesRejectsInvalidJoinReferences(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Symbol: "teams"}
	join := declaration.Join{Kind: declaration.JoinInner, Table: teams, On: declaration.JoinCondition{
		Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"},
	}}
	base := postgres.SchemaMember{MemberID: "accounts.user/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts", Tables: []declaration.Table{
			{ID: "users", Columns: []declaration.Column{{ID: "team-id", Type: declaration.TypeInt64}}},
			{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}},
		},
	}}
	for _, test := range []struct {
		name string
		edit func(*postgres.SchemaMember, *declaration.Join)
		want string
	}{
		{"missing left column", func(_ *postgres.SchemaMember, join *declaration.Join) { join.On.Left.Column = "missing" }, "join left column missing"},
		{"missing right column", func(_ *postgres.SchemaMember, join *declaration.Join) { join.On.Right.Column = "missing" }, "join right column missing"},
		{"mismatched types", func(member *postgres.SchemaMember, _ *declaration.Join) {
			member.Model.Tables[1].Columns[0].Type = declaration.TypeString
		}, "join column types do not match"},
		{"missing target table", func(_ *postgres.SchemaMember, join *declaration.Join) { join.Table.Symbol = "missing" }, "join table"},
	} {
		t.Run(test.name, func(t *testing.T) {
			member := base
			member.Model.Tables = append([]declaration.Table(nil), base.Model.Tables...)
			member.Model.Tables[1].Columns = append([]declaration.Column(nil), base.Model.Tables[1].Columns...)
			candidate := join
			test.edit(&member, &candidate)
			member.Model.Queries = []declaration.Query{{ID: "read", Operation: declaration.OperationSelect, Table: users, Joins: []declaration.Join{candidate}}}
			_, err := postgres.CompileQueries([]postgres.SchemaMember{member})
			if !errors.Is(err, postgres.ErrQueryCompilation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCompileQueriesRejectsCrossResourceJoin(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Member: "teams.member/v1", Symbol: "teams"}
	owner := postgres.SchemaMember{MemberID: "accounts.user/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts", Imports: []string{"teams.member/v1"},
		Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "team-id", Type: declaration.TypeInt64}}}},
		Queries: []declaration.Query{{ID: "read", Operation: declaration.OperationSelect, Table: users, Joins: []declaration.Join{{
			Kind: declaration.JoinInner, Table: teams, On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"}},
		}}}},
	}}
	other := postgres.SchemaMember{MemberID: "teams.member/v1", Resource: "database.secondary", Model: declaration.Model{
		Namespace: "teams", Tables: []declaration.Table{{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}},
	}}
	_, err := postgres.CompileQueries([]postgres.SchemaMember{owner, other})
	if !errors.Is(err, postgres.ErrCrossResourceReference) {
		t.Fatalf("error = %v, want cross-Resource reference", err)
	}
}
