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
