package declaration_test

import (
	"errors"
	"testing"

	"github.com/plystra/data/declaration"
)

type access struct{}

func TestNormalizeSortsAndDigestsTheLogicalModel(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Imports:   []string{"authz.persistence/v1", "audit.events/v1"},
		Tables: []declaration.Table{
			{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}, {ID: "email", Type: declaration.TypeString}}},
			{ID: "sessions", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}}},
		},
		Queries:    []declaration.Query{{ID: "find-account", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}}},
		Plans:      []declaration.Plan{{ID: "create-account", Statements: []declaration.PlanStatement{{ID: "insert", Query: declaration.Ref{Symbol: "find-account"}, Cardinality: declaration.CardinalityNone, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}}}}},
		Migrations: []declaration.Migration{{ID: "create-accounts", Version: 1, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "accounts"}}}}},
	}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := model.Imports[0]; got != "audit.events/v1" {
		t.Fatalf("imports were not normalized: %v", model.Imports)
	}
	if got := model.Tables[0].ID; got != "accounts" {
		t.Fatalf("tables were not normalized: %q", got)
	}
	first, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	reordered := member
	reordered.Imports = []string{"audit.events/v1", "authz.persistence/v1"}
	reordered.Tables = []declaration.Table{member.Tables[1], member.Tables[0]}
	second, err := reordered.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("equivalent declaration digests differ: %s != %s", first, second)
	}
}

func TestNormalizeRejectsInvalidAndHiddenReferences(t *testing.T) {
	tests := []struct {
		name   string
		member declaration.Member[access]
		want   error
	}{
		{name: "namespace", member: declaration.Member[access]{Namespace: "Authn"}, want: declaration.ErrInvalidSchema},
		{name: "duplicate symbol", member: declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "accounts"}}, Queries: []declaration.Query{{ID: "accounts", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}}}}, want: declaration.ErrDuplicate},
		{name: "unimported reference", member: declaration.Member[access]{Namespace: "authn", Queries: []declaration.Query{{ID: "find", Operation: declaration.OperationSelect, Table: declaration.Ref{Member: "authz.persistence/v1", Symbol: "accounts"}}}}, want: declaration.ErrReference},
		{name: "unresolved local reference", member: declaration.Member[access]{Namespace: "authn", Queries: []declaration.Query{{ID: "find", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}}}}, want: declaration.ErrReference},
		{name: "invalid symbol", member: declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "bad--symbol"}}}, want: declaration.ErrInvalidID},
		{name: "unsupported predicate", member: declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "accounts"}}, Queries: []declaration.Query{{ID: "find", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}, Predicate: declaration.Predicate{Kind: "contains", Left: "email"}}}}, want: declaration.ErrUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.member.Normalize()
			if !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeDoesNotMutateAuthoredSlices(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Imports:   []string{"z.audit/v1", "a.authz/v1"},
		Tables:    []declaration.Table{{ID: "z-table", Columns: []declaration.Column{{ID: "z-column", Type: declaration.TypeString}, {ID: "a-column", Type: declaration.TypeString}}}, {ID: "a-table"}},
	}
	if _, err := member.Normalize(); err != nil {
		t.Fatal(err)
	}
	if member.Imports[0] != "z.audit/v1" || member.Tables[0].ID != "z-table" || member.Tables[0].Columns[0].ID != "z-column" {
		t.Fatal("Normalize mutated authored slices")
	}
}

func TestNormalizeQueryParametersAndAssignments(t *testing.T) {
	member := declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "accounts"}}, Queries: []declaration.Query{{
		ID: "update", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"},
		Parameters:  []declaration.Parameter{{ID: "z-id", Type: declaration.TypeInt64}, {ID: "a-email", Type: declaration.TypeString}},
		Assignments: []declaration.Assignment{{Column: "z-column", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "a-email"}}, {Column: "a-column", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "a-email"}}},
		Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "z-id"}},
	}}}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if model.Queries[0].Parameters[0].ID != "a-email" || model.Queries[0].Assignments[0].Column != "a-column" || member.Queries[0].Parameters[0].ID != "z-id" || member.Queries[0].Assignments[0].Column != "z-column" {
		t.Fatalf("normalization or authored isolation failed: model=%#v authored=%#v", model.Queries[0], member.Queries[0])
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"duplicate parameter", func(query *declaration.Query) { query.Parameters[1].ID = "z-id" }, declaration.ErrDuplicate},
		{"unknown parameter", func(query *declaration.Query) { query.Assignments[0].Value.Parameter = "missing" }, declaration.ErrInvalidSchema},
		{"type mismatch", func(query *declaration.Query) { query.Assignments[0].Value.Kind = declaration.TypeInt64 }, declaration.ErrInvalidSchema},
		{"unbounded update", func(query *declaration.Query) { query.Predicate = declaration.Predicate{} }, declaration.ErrInvalidSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			changed.Queries[0].Parameters = append([]declaration.Parameter(nil), member.Queries[0].Parameters...)
			changed.Queries[0].Assignments = append([]declaration.Assignment(nil), member.Queries[0].Assignments...)
			test.edit(&changed.Queries[0])
			_, err := changed.Normalize()
			if !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeQueryReturningResults(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}}}},
		Queries: []declaration.Query{
			{ID: "find", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}, Projection: []declaration.Projection{{Column: "id", Alias: "account-id"}, {Column: "email"}}, Results: []declaration.ResultField{{ID: "id", Source: "account-id", Type: declaration.TypeInt64}, {ID: "email", Source: "email", Type: declaration.TypeString, Nullable: true}}},
			{ID: "update", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"}, Assignments: []declaration.Assignment{{Column: "email", Value: declaration.Value{Kind: declaration.TypeString, Set: true, String: "new"}}}, Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Int64: 1}}, Returning: []declaration.Projection{{Column: "id", Alias: "account-id"}}, Results: []declaration.ResultField{{ID: "id", Source: "account-id", Type: declaration.TypeInt64}}},
		},
	}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Queries[0].Results) != 2 || model.Queries[1].Returning[0].Alias != "account-id" {
		t.Fatalf("normalized result intent = %#v", model.Queries)
	}

	tests := []struct {
		name string
		edit func(*declaration.Query)
	}{
		{"select returning", func(query *declaration.Query) { query.Returning = []declaration.Projection{{Column: "id"}} }},
		{"missing explicit output", func(query *declaration.Query) {
			query.Projection = nil
			query.Results = []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}}
		}},
		{"incomplete mutation mapping", func(query *declaration.Query) { query.Results = nil }},
		{"duplicate source", func(query *declaration.Query) { query.Results[1].Source = query.Results[0].Source }},
		{"unknown source", func(query *declaration.Query) { query.Results[0].Source = "missing" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			changed.Queries[0].Projection = append([]declaration.Projection(nil), member.Queries[0].Projection...)
			changed.Queries[0].Results = append([]declaration.ResultField(nil), member.Queries[0].Results...)
			changed.Queries[1].Returning = append([]declaration.Projection(nil), member.Queries[1].Returning...)
			changed.Queries[1].Results = append([]declaration.ResultField(nil), member.Queries[1].Results...)
			test.edit(&changed.Queries[0])
			if test.name == "incomplete mutation mapping" {
				changed.Queries[0] = member.Queries[1]
				test.edit(&changed.Queries[0])
			}
			if _, err := changed.Normalize(); err == nil {
				t.Fatal("Normalize accepted invalid result mapping")
			}
		})
	}
}

func TestNormalizeClonesQueryResultSlices(t *testing.T) {
	member := declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "accounts"}}, Queries: []declaration.Query{{
		ID: "find", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Projection: []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}}}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	model.Queries[0].Projection[0].Column = "changed"
	model.Queries[0].Results[0].Source = "changed"
	if member.Queries[0].Projection[0].Column != "id" || member.Queries[0].Results[0].Source != "id" {
		t.Fatal("Normalize did not isolate query result slices")
	}
}

func TestNormalizeJoinsPreservesOrderAndClones(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Symbol: "teams"}
	departments := declaration.Ref{Symbol: "departments"}
	member := declaration.Member[access]{Namespace: "accounts", Tables: []declaration.Table{{ID: "users"}, {ID: "teams"}, {ID: "departments"}}, Queries: []declaration.Query{{
		ID: "read", Operation: declaration.OperationSelect, Table: users,
		Joins: []declaration.Join{
			{Kind: declaration.JoinInner, Table: teams, On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"}}},
			{Kind: declaration.JoinLeft, Table: departments, On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: teams, Column: "department-id"}, Right: declaration.ColumnRef{Table: departments, Column: "id"}}},
		},
		Projection: []declaration.Projection{{Table: teams, Column: "id", Alias: "department-id"}},
	}}}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Queries[0].Joins) != 2 || model.Queries[0].Joins[0].Table != teams || model.Queries[0].Joins[1].Kind != declaration.JoinLeft {
		t.Fatalf("normalized joins = %#v", model.Queries[0].Joins)
	}
	if model.Queries[0].Projection[0].Table != teams {
		t.Fatalf("normalized projection = %#v", model.Queries[0].Projection)
	}
	model.Queries[0].Joins[0].On.Left.Column = "changed"
	if member.Queries[0].Joins[0].On.Left.Column != "team-id" {
		t.Fatal("Normalize did not isolate joins")
	}
	first, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	member.Queries[0].Joins[0].On.Left.Column = "other-id"
	second, err := member.Digest()
	if err != nil || first == second {
		t.Fatalf("join condition did not affect digest: %s, %s, %v", first, second, err)
	}
}

func TestNormalizeRejectsInvalidJoins(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Symbol: "teams"}
	valid := declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: users, Joins: []declaration.Join{{
		Kind: declaration.JoinInner, Table: teams,
		On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"}},
	}}}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"mutation", func(query *declaration.Query) { query.Operation = declaration.OperationInsert }, declaration.ErrInvalidSchema},
		{"kind", func(query *declaration.Query) { query.Joins[0].Kind = "cross" }, declaration.ErrUnsupported},
		{"missing local table", func(query *declaration.Query) { query.Joins[0].Table.Symbol = "missing" }, declaration.ErrReference},
		{"unimported table", func(query *declaration.Query) { query.Joins[0].Table.Member = "other.member/v1" }, declaration.ErrReference},
		{"duplicate table", func(query *declaration.Query) { query.Joins[0].Table = users }, declaration.ErrDuplicate},
		{"forward left table", func(query *declaration.Query) { query.Joins[0].On.Left.Table = teams }, declaration.ErrReference},
		{"wrong right table", func(query *declaration.Query) { query.Joins[0].On.Right.Table = users }, declaration.ErrReference},
		{"invalid left column", func(query *declaration.Query) { query.Joins[0].On.Left.Column = "Bad" }, declaration.ErrReference},
		{"unintroduced projection table", func(query *declaration.Query) {
			query.Projection = []declaration.Projection{{Table: declaration.Ref{Symbol: "missing"}, Column: "id"}}
		}, declaration.ErrReference},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := valid
			query.Joins = append([]declaration.Join(nil), valid.Joins...)
			test.edit(&query)
			member := declaration.Member[access]{Namespace: "accounts", Tables: []declaration.Table{{ID: "users"}, {ID: "teams"}}, Queries: []declaration.Query{query}}
			_, err := member.Normalize()
			if !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}
