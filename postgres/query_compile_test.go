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
	want := "-- plystra.data.postgres-query/v1 accounts.user/v1#list-active\n" +
		`SELECT "id", "email" AS "address" FROM "accounts"."users" WHERE ("active" = TRUE) AND ("id" > 10) ORDER BY "email" ASC LIMIT 25 OFFSET 5;` + "\n"
	if first[0].SQL != want {
		t.Fatalf("single-table SQL = %q, want %q", first[0].SQL, want)
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
			Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
		},
		{
			ID: "delete", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "users"},
			Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
			Predicate:  declaration.Predicate{Kind: declaration.PredicateEqual, Left: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
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
					Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: "id", Value: declaration.Int64(1)},
					Returning:   []declaration.Projection{{Column: "email"}},
					Results:     []declaration.ResultField{{ID: "email", Source: "email", Type: declaration.TypeString, Nullable: true}},
				},
				{
					ID: "delete-user", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "users"},
					Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: "id", Value: declaration.Int64(1)},
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
		{name: "unknown projection", query: declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Projection: []declaration.Projection{{Column: "missing"}}}, want: "projection column missing"},
		{name: "unknown predicate", query: declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"}, Predicate: declaration.Predicate{Kind: declaration.PredicateIsNull, Left: "missing"}}, want: "predicate column missing"},
	} {
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
				Predicate:  declaration.Predicate{Kind: declaration.PredicateEqual, Left: "active", Value: declaration.Bool(true)},
				Ordering: []declaration.Ordering{
					{Column: "id", Direction: declaration.OrderingAscending},
					{Table: regions, Column: "id", Direction: declaration.OrderingDescending},
				},
			},
			{ID: "remote-regions", Operation: declaration.OperationSelect, Table: regions, Projection: []declaration.Projection{{Column: "id"}}},
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
				`SELECT "t0"."id" AS "user-id", "t2"."id" AS "region-id" FROM "accounts"."users" AS "t0" INNER JOIN "accounts"."teams" AS "t1" ON "t0"."team-id" = "t1"."id" LEFT JOIN "geo"."regions" AS "t2" ON "t1"."region-id" = "t2"."id" WHERE "t0"."active" = TRUE ORDER BY "t0"."id" ASC, "t2"."id" DESC;` + "\n"
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
