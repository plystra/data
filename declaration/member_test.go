package declaration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/plystra/data/declaration"
)

type access struct{}

func TestQueryWithoutHavingPreservesLogicalDigest(t *testing.T) {
	query := declaration.Query{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}}
	encoded, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"having"`)) {
		t.Fatalf("query without HAVING gained JSON field: %s", encoded)
	}
	digest, err := declaration.DigestModel(declaration.Model{Namespace: "accounts", Queries: []declaration.Query{query}})
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:b312f19da8c2ff3c48547ca809bae3ed1eba122864311e78ae89597066fb57f9"
	if digest != want {
		t.Fatalf("logical digest = %s, want %s", digest, want)
	}
}

func TestNormalizeSortsAndDigestsTheLogicalModel(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Imports:   []string{"authz.persistence/v1", "audit.events/v1"},
		Tables: []declaration.Table{
			{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}, {ID: "email", Type: declaration.TypeString}}},
			{ID: "sessions", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}}},
		},
		Queries:    []declaration.Query{{ID: "find-account", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}}},
		Plans:      []declaration.Plan{{ID: "create-account", Statements: []declaration.PlanStatement{{ID: "read", Query: declaration.Ref{Symbol: "find-account"}, Cardinality: declaration.CardinalityMany}}}},
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
		{name: "duplicate constraint", member: declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "accounts-key", Kind: declaration.ConstraintUnique, Columns: []string{"id"}}, {ID: "accounts-key", Kind: declaration.ConstraintForeignKey, Columns: []string{"id"}, ReferencedTable: declaration.Ref{Symbol: "accounts"}, ReferencedCols: []string{"id"}}}}}}, want: declaration.ErrDuplicate},
		{name: "unsupported predicate", member: declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "accounts"}}, Queries: []declaration.Query{{ID: "find", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}, Predicate: declaration.Predicate{Kind: "contains", Left: declaration.ColumnRef{Column: "email"}}}}}, want: declaration.ErrUnsupported},
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

func TestNormalizeRequiresPlanAssertionsMatchingQueryOperation(t *testing.T) {
	base := func(query declaration.Query, statement declaration.PlanStatement) declaration.Member[access] {
		return declaration.Member[access]{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "name", Type: declaration.TypeString}}}},
			Queries:   []declaration.Query{query},
			Plans:     []declaration.Plan{{ID: "read-plan", Statements: []declaration.PlanStatement{statement}}},
		}
	}
	selectQuery := declaration.Query{
		ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
		Projection: []declaration.Projection{{Column: "id"}},
		Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}
	insertQuery := declaration.Query{
		ID: "insert", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"},
		Assignments: []declaration.Assignment{{Column: "name", Value: declaration.String("Ada")}},
	}
	tests := []struct {
		name   string
		member declaration.Member[access]
	}{
		{
			name:   "select requires cardinality",
			member: base(selectQuery, declaration.PlanStatement{ID: "read", Query: declaration.Ref{Symbol: "read"}}),
		},
		{
			name: "select rejects affected rows",
			member: base(selectQuery, declaration.PlanStatement{ID: "read", Query: declaration.Ref{Symbol: "read"}, Cardinality: declaration.CardinalityMany,
				Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}}),
		},
		{
			name:   "mutation requires affected rows",
			member: base(insertQuery, declaration.PlanStatement{ID: "insert", Query: declaration.Ref{Symbol: "insert"}}),
		},
		{
			name: "mutation rejects cardinality",
			member: base(insertQuery, declaration.PlanStatement{ID: "insert", Query: declaration.Ref{Symbol: "insert"}, Cardinality: declaration.CardinalityMany,
				Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.member.Normalize(); !errors.Is(err, declaration.ErrInvalidSchema) {
				t.Fatalf("Normalize() error = %v, want ErrInvalidSchema", err)
			}
		})
	}
}

func TestNormalizeQueryParametersAndAssignments(t *testing.T) {
	member := declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "accounts"}}, Queries: []declaration.Query{{
		ID: "update", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"},
		Parameters:  []declaration.Parameter{{ID: "z-id", Type: declaration.TypeInt64}, {ID: "a-email", Type: declaration.TypeString}},
		Assignments: []declaration.Assignment{{Column: "z-column", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "a-email"}}, {Column: "a-column", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "a-email"}}},
		Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "z-id"}},
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

func TestNormalizeExpectedRevisionIncrement(t *testing.T) {
	member := declaration.Member[access]{Namespace: "authn", Tables: []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "revision", Type: declaration.TypeInt64}, {ID: "name", Type: declaration.TypeString}}}}, Queries: []declaration.Query{{
		ID: "revise", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"},
		Assignments: []declaration.Assignment{{Column: "revision", Increment: true}},
		Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "revision"}, Value: declaration.Int64(4)},
	}}}
	model, err := member.Normalize()
	if err != nil || !model.Queries[0].Assignments[0].Increment {
		t.Fatalf("normalized revision update = %#v, %v", model, err)
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Member[access])
	}{
		{"insert", func(m *declaration.Member[access]) {
			m.Queries[0].Operation = declaration.OperationInsert
			m.Queries[0].Predicate = declaration.Predicate{}
		}},
		{"mixed value", func(m *declaration.Member[access]) { m.Queries[0].Assignments[0].Value = declaration.Int64(1) }},
		{"wrong type", func(m *declaration.Member[access]) { m.Tables[0].Columns[1].Type = declaration.TypeString }},
		{"nullable", func(m *declaration.Member[access]) { m.Tables[0].Columns[1].Nullable = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Tables = append([]declaration.Table(nil), member.Tables...)
			changed.Tables[0].Columns = append([]declaration.Column(nil), member.Tables[0].Columns...)
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			changed.Queries[0].Assignments = append([]declaration.Assignment(nil), member.Queries[0].Assignments...)
			test.edit(&changed)
			if _, err := changed.Normalize(); !errors.Is(err, declaration.ErrInvalidSchema) {
				t.Fatalf("increment validation = %v", err)
			}
		})
	}
}

func TestNormalizeInsertConflictByNamedConstraint(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "accounts-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}, {ID: "accounts-fk", Kind: declaration.ConstraintForeignKey, Columns: []string{"id"}, ReferencedTable: declaration.Ref{Symbol: "accounts"}, ReferencedCols: []string{"id"}}}}},
		Queries:   []declaration.Query{{ID: "insert", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "accounts"}, Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Int64(1)}}, Conflict: declaration.Conflict{Constraint: "accounts-pk", Action: declaration.ConflictDoNothing}}},
	}
	model, err := member.Normalize()
	if err != nil || model.Queries[0].Conflict.Constraint != "accounts-pk" {
		t.Fatalf("normalized conflict = %#v, %v", model, err)
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"unknown constraint", func(query *declaration.Query) { query.Conflict.Constraint = "missing" }, declaration.ErrReference},
		{"foreign key", func(query *declaration.Query) { query.Conflict.Constraint = "accounts-fk" }, declaration.ErrReference},
		{"missing update columns", func(query *declaration.Query) { query.Conflict.Action = declaration.ConflictDoUpdate }, declaration.ErrInvalidSchema},
		{"select conflict", func(query *declaration.Query) { query.Operation = declaration.OperationSelect }, declaration.ErrInvalidSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			test.edit(&changed.Queries[0])
			_, err := changed.Normalize()
			if !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeConflictUpdateColumns(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables: []declaration.Table{{ID: "accounts", Columns: []declaration.Column{
			{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}, {ID: "name", Type: declaration.TypeString},
		}, Constraints: []declaration.Constraint{{ID: "accounts-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}}},
		Queries: []declaration.Query{{ID: "insert", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "accounts"},
			Assignments: []declaration.Assignment{{Column: "name", Value: declaration.String("N")}, {Column: "id", Value: declaration.Int64(1)}, {Column: "email", Value: declaration.String("e")}},
			Conflict:    declaration.Conflict{Constraint: "accounts-pk", Action: declaration.ConflictDoUpdate, UpdateColumns: []string{"name", "email"}},
		}},
	}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := model.Queries[0].Conflict.UpdateColumns; len(got) != 2 || got[0] != "email" || got[1] != "name" || member.Queries[0].Conflict.UpdateColumns[0] != "name" {
		t.Fatalf("normalized update columns = %v", got)
	}
	first, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	reordered := member
	reordered.Queries = append([]declaration.Query(nil), member.Queries...)
	reordered.Queries[0].Conflict.UpdateColumns = []string{"email", "name"}
	second, err := reordered.Digest()
	if err != nil || first != second {
		t.Fatalf("equivalent update digests = %q, %q, %v", first, second, err)
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"duplicate", func(query *declaration.Query) { query.Conflict.UpdateColumns = []string{"email", "email"} }, declaration.ErrDuplicate},
		{"unassigned", func(query *declaration.Query) { query.Conflict.UpdateColumns = []string{"id", "missing"} }, declaration.ErrReference},
		{"absent target column", func(query *declaration.Query) {
			query.Assignments = append(query.Assignments, declaration.Assignment{Column: "missing", Value: declaration.String("x")})
			query.Conflict.UpdateColumns = []string{"missing"}
		}, declaration.ErrReference},
		{"empty", func(query *declaration.Query) { query.Conflict.UpdateColumns = nil }, declaration.ErrInvalidSchema},
		{"do nothing columns", func(query *declaration.Query) { query.Conflict.Action = declaration.ConflictDoNothing }, declaration.ErrInvalidSchema},
		{"unknown action", func(query *declaration.Query) { query.Conflict.Action = "replace" }, declaration.ErrInvalidSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
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
			{ID: "update", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"}, Assignments: []declaration.Assignment{{Column: "email", Value: declaration.Value{Kind: declaration.TypeString, Set: true, String: "new"}}}, Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Int64: 1}}, Returning: []declaration.Projection{{Column: "id", Alias: "account-id"}}, Results: []declaration.ResultField{{ID: "id", Source: "account-id", Type: declaration.TypeInt64}}},
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
		{"missing select mapping", func(query *declaration.Query) { query.Results = nil }},
		{"incomplete select mapping", func(query *declaration.Query) { query.Results = query.Results[:1] }},
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

func TestNormalizeCountProjection(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}},
		Queries: []declaration.Query{{
			ID: "count-accounts", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			Projection: []declaration.Projection{{Aggregate: declaration.AggregateCount, Alias: "total"}},
			Results:    []declaration.ResultField{{ID: "total", Source: "total", Type: declaration.TypeInt64}},
		}},
	}
	if _, err := member.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"missing alias", func(query *declaration.Query) { query.Projection[0].Alias = "" }, declaration.ErrInvalidSchema},
		{"missing result", func(query *declaration.Query) { query.Results = nil }, declaration.ErrInvalidSchema},
		{"wrong result type", func(query *declaration.Query) { query.Results[0].Type = declaration.TypeString }, declaration.ErrInvalidSchema},
		{"nullable result", func(query *declaration.Query) { query.Results[0].Nullable = true }, declaration.ErrInvalidSchema},
		{"count star table", func(query *declaration.Query) { query.Projection[0].Table = declaration.Ref{Symbol: "accounts"} }, declaration.ErrInvalidSchema},
		{"unsupported aggregate", func(query *declaration.Query) { query.Projection[0].Aggregate = "avg" }, declaration.ErrUnsupported},
		{"mixed projection", func(query *declaration.Query) {
			query.Projection = append(query.Projection, declaration.Projection{Column: "id"})
			query.Results = append(query.Results, declaration.ResultField{ID: "id", Source: "id", Type: declaration.TypeInt64})
		}, declaration.ErrUnsupported},
		{"aggregate modifiers", func(query *declaration.Query) { query.HasLimit = true; query.Limit = 1 }, declaration.ErrUnsupported},
		{"aggregate offset", func(query *declaration.Query) { query.HasOffset = true; query.Offset = 1 }, declaration.ErrUnsupported},
		{"mutation aggregate", func(query *declaration.Query) {
			query.Operation = declaration.OperationInsert
			query.Projection = nil
			query.Returning = append([]declaration.Projection(nil), query.Returning...)
			if len(query.Returning) == 0 {
				query.Returning = []declaration.Projection{{Aggregate: declaration.AggregateCount, Alias: "total"}}
			}
		}, declaration.ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			changed.Queries[0].Projection = append([]declaration.Projection(nil), member.Queries[0].Projection...)
			changed.Queries[0].Results = append([]declaration.ResultField(nil), member.Queries[0].Results...)
			test.edit(&changed.Queries[0])
			if _, err := changed.Normalize(); !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeMinProjection(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "score", Type: declaration.TypeInt64}, {ID: "label", Type: declaration.TypeString}}}},
		Queries: []declaration.Query{{
			ID: "min-score", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			Projection: []declaration.Projection{{Aggregate: declaration.AggregateMin, Column: "score", Alias: "minimum"}},
			Results:    []declaration.ResultField{{ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}},
		}},
	}
	if _, err := member.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"missing column", func(query *declaration.Query) { query.Projection[0].Column = "" }, declaration.ErrInvalidSchema},
		{"wrong result nullability", func(query *declaration.Query) { query.Results[0].Nullable = false }, declaration.ErrInvalidSchema},
		{"wrong result type", func(query *declaration.Query) { query.Results[0].Type = declaration.TypeString }, declaration.ErrInvalidSchema},
		{"unsupported column type", func(query *declaration.Query) { query.Projection[0].Column = "label" }, declaration.ErrInvalidSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			changed.Queries[0].Projection = append([]declaration.Projection(nil), member.Queries[0].Projection...)
			changed.Queries[0].Results = append([]declaration.ResultField(nil), member.Queries[0].Results...)
			test.edit(&changed.Queries[0])
			if _, err := changed.Normalize(); !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeSumProjection(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables: []declaration.Table{{ID: "accounts", Columns: []declaration.Column{
			{ID: "amount", Type: declaration.TypeFloat64}, {ID: "count", Type: declaration.TypeInt64},
		}}},
		Queries: []declaration.Query{{
			ID: "sum-amount", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			Projection: []declaration.Projection{{Aggregate: declaration.AggregateSum, Column: "amount", Alias: "total"}},
			Results:    []declaration.ResultField{{ID: "total", Source: "total", Type: declaration.TypeFloat64, Nullable: true}},
		}},
	}
	if _, err := member.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
	}{
		{"missing column", func(query *declaration.Query) { query.Projection[0].Column = "" }},
		{"integer input", func(query *declaration.Query) { query.Projection[0].Column = "count" }},
		{"integer result", func(query *declaration.Query) { query.Results[0].Type = declaration.TypeInt64 }},
		{"non-null result", func(query *declaration.Query) { query.Results[0].Nullable = false }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			changed.Queries[0].Projection = append([]declaration.Projection(nil), member.Queries[0].Projection...)
			changed.Queries[0].Results = append([]declaration.ResultField(nil), member.Queries[0].Results...)
			test.edit(&changed.Queries[0])
			if _, err := changed.Normalize(); !errors.Is(err, declaration.ErrInvalidSchema) {
				t.Fatalf("SUM rejection = %v", err)
			}
		})
	}
}

func TestNormalizeMaxProjection(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "score", Type: declaration.TypeInt64}, {ID: "label", Type: declaration.TypeString}}}},
		Queries: []declaration.Query{{
			ID: "max-score", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			Projection: []declaration.Projection{{Aggregate: declaration.AggregateMax, Column: "score", Alias: "maximum"}},
			Results:    []declaration.ResultField{{ID: "maximum", Source: "maximum", Type: declaration.TypeInt64, Nullable: true}},
		}},
	}
	if _, err := member.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"missing column", func(query *declaration.Query) { query.Projection[0].Column = "" }, declaration.ErrInvalidSchema},
		{"wrong result nullability", func(query *declaration.Query) { query.Results[0].Nullable = false }, declaration.ErrInvalidSchema},
		{"wrong result type", func(query *declaration.Query) { query.Results[0].Type = declaration.TypeString }, declaration.ErrInvalidSchema},
		{"unsupported column type", func(query *declaration.Query) { query.Projection[0].Column = "label" }, declaration.ErrInvalidSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			changed.Queries[0].Projection = append([]declaration.Projection(nil), member.Queries[0].Projection...)
			changed.Queries[0].Results = append([]declaration.ResultField(nil), member.Queries[0].Results...)
			test.edit(&changed.Queries[0])
			if _, err := changed.Normalize(); !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeGroupedAggregate(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables: []declaration.Table{{ID: "accounts", Columns: []declaration.Column{
			{ID: "region", Type: declaration.TypeString}, {ID: "tier", Type: declaration.TypeString}, {ID: "id", Type: declaration.TypeInt64},
		}}},
		Queries: []declaration.Query{{
			ID: "count-by-region", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			GroupBy: []declaration.ColumnRef{{Column: "region"}, {Column: "tier"}},
			Projection: []declaration.Projection{
				{Column: "region"}, {Column: "tier"}, {Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"},
			},
			Having: declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Int64(1)},
			Results: []declaration.ResultField{
				{ID: "region", Source: "region", Type: declaration.TypeString},
				{ID: "tier", Source: "tier", Type: declaration.TypeString},
				{ID: "total", Source: "total", Type: declaration.TypeInt64},
			},
		}},
	}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Queries[0].GroupBy) != 2 || model.Queries[0].GroupBy[0].Column != "region" || model.Queries[0].GroupBy[1].Column != "tier" {
		t.Fatalf("normalized grouping = %#v", model.Queries[0].GroupBy)
	}
	if model.Queries[0].Having.Left.Column != "total" {
		t.Fatalf("normalized having = %#v", model.Queries[0].Having)
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"duplicate grouping columns", func(query *declaration.Query) {
			query.GroupBy = append(query.GroupBy, declaration.ColumnRef{Column: "region"})
			query.Projection = append(query.Projection, declaration.Projection{Column: "region"})
			query.Results = append(query.Results, declaration.ResultField{ID: "region-copy", Source: "region", Type: declaration.TypeString})
		}, declaration.ErrDuplicate},
		{"grouping without aggregate", func(query *declaration.Query) {
			query.Projection = []declaration.Projection{{Column: "region"}}
			query.Results = []declaration.ResultField{{ID: "region", Source: "region", Type: declaration.TypeString}}
		}, declaration.ErrUnsupported},
		{"grouping column not projected", func(query *declaration.Query) {
			query.Projection = []declaration.Projection{{Column: "region"}, {Column: "id"}, {Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}}
			query.Results = []declaration.ResultField{{ID: "region", Source: "region", Type: declaration.TypeString}, {ID: "id", Source: "id", Type: declaration.TypeInt64}, {ID: "total", Source: "total", Type: declaration.TypeInt64}}
		}, declaration.ErrReference},
		{"having output not projected", func(query *declaration.Query) {
			query.Having.Left.Column = "missing"
		}, declaration.ErrReference},
		{"having cannot name table", func(query *declaration.Query) {
			query.Having.Left.Table = declaration.Ref{Symbol: "accounts"}
		}, declaration.ErrUnsupported},
		{"having requires a kind", func(query *declaration.Query) {
			query.Having.Kind = ""
		}, declaration.ErrInvalidSchema},
		{"having requires grouping", func(query *declaration.Query) {
			query.GroupBy = nil
		}, declaration.ErrUnsupported},
		{"having nested item requires a kind", func(query *declaration.Query) {
			query.Having = declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{{Left: declaration.ColumnRef{Column: "total"}}, {Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "total"}}}}
		}, declaration.ErrUnsupported},
		{"having boolean node cannot carry operands", func(query *declaration.Query) {
			query.Having = declaration.Predicate{Kind: declaration.PredicateAnd, Left: declaration.ColumnRef{Column: "total"}, Items: []declaration.Predicate{{Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "total"}}, {Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "total"}}}}
		}, declaration.ErrInvalidSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := member
			changed.Queries = append([]declaration.Query(nil), member.Queries...)
			changed.Queries[0].GroupBy = append([]declaration.ColumnRef(nil), member.Queries[0].GroupBy...)
			changed.Queries[0].Projection = append([]declaration.Projection(nil), member.Queries[0].Projection...)
			changed.Queries[0].Results = append([]declaration.ResultField(nil), member.Queries[0].Results...)
			test.edit(&changed.Queries[0])
			if _, err := changed.Normalize(); !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeGroupedAggregateOrdering(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "region", Type: declaration.TypeString}, {ID: "id", Type: declaration.TypeInt64}}}},
		Queries: []declaration.Query{{
			ID: "count-by-region", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			GroupBy:    []declaration.ColumnRef{{Column: "region"}},
			Projection: []declaration.Projection{{Column: "region", Alias: "zone"}, {Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}},
			Results:    []declaration.ResultField{{ID: "region", Source: "zone", Type: declaration.TypeString}, {ID: "total", Source: "total", Type: declaration.TypeInt64}},
			Ordering:   []declaration.Ordering{{Column: "total", Direction: declaration.OrderingDescending}, {Column: "zone", Direction: declaration.OrderingAscending}},
		}},
	}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Queries[0].Ordering) != 2 || model.Queries[0].Ordering[0].Column != "total" || model.Queries[0].Ordering[1].Column != "zone" {
		t.Fatalf("normalized ordering = %#v", model.Queries[0].Ordering)
	}
	bounded := member
	bounded.Queries = append([]declaration.Query(nil), member.Queries...)
	bounded.Queries[0].HasLimit, bounded.Queries[0].Limit = true, 2
	bounded.Queries[0].HasOffset, bounded.Queries[0].Offset = true, 1
	boundedModel, err := bounded.Normalize()
	if err != nil || !boundedModel.Queries[0].HasLimit || boundedModel.Queries[0].Limit != 2 || !boundedModel.Queries[0].HasOffset || boundedModel.Queries[0].Offset != 1 {
		t.Fatalf("bounded grouped query = %#v, %v", boundedModel, err)
	}
	baselineDigest, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	boundedDigest, err := bounded.Digest()
	if err != nil || boundedDigest == baselineDigest {
		t.Fatalf("bounded grouped digest = %q, baseline = %q, error = %v", boundedDigest, baselineDigest, err)
	}
	bounded.Queries[0].Limit, bounded.Queries[0].Offset = 0, 0
	if zero, err := bounded.Normalize(); err != nil || !zero.Queries[0].HasLimit || !zero.Queries[0].HasOffset {
		t.Fatalf("explicit zero bounds = %#v, %v", zero, err)
	}
	member.Queries[0].Ordering[0].Column = "zone"
	if model.Queries[0].Ordering[0].Column != "total" {
		t.Fatal("normalized ordering changed with authored input")
	}
	for _, test := range []struct {
		name string
		edit func(*declaration.Query)
		want error
	}{
		{"missing output", func(query *declaration.Query) { query.Ordering[0].Column = "missing" }, declaration.ErrReference},
		{"qualified output", func(query *declaration.Query) { query.Ordering[0].Table = query.Table }, declaration.ErrUnsupported},
		{"ungrouped aggregate", func(query *declaration.Query) {
			query.GroupBy = nil
			query.Projection = query.Projection[1:]
			query.Results = query.Results[1:]
		}, declaration.ErrUnsupported},
		{"paged without grouping key order", func(query *declaration.Query) {
			query.HasLimit, query.Limit = true, 1
			query.Ordering = query.Ordering[:1]
		}, declaration.ErrUnsupported},
		{"offset without ordering", func(query *declaration.Query) {
			query.HasOffset, query.Offset = true, 1
			query.Ordering = nil
		}, declaration.ErrUnsupported},
		{"disabled limit with value", func(query *declaration.Query) { query.Limit = 1 }, declaration.ErrInvalidSchema},
		{"disabled offset with value", func(query *declaration.Query) { query.Offset = 1 }, declaration.ErrInvalidSchema},
		{"limit beyond PostgreSQL range", func(query *declaration.Query) { query.HasLimit, query.Limit = true, ^uint64(0) }, declaration.ErrInvalidSchema},
		{"offset beyond PostgreSQL range", func(query *declaration.Query) { query.HasOffset, query.Offset = true, ^uint64(0) }, declaration.ErrInvalidSchema},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := model.Queries[0]
			query.Ordering = append([]declaration.Ordering(nil), query.Ordering...)
			test.edit(&query)
			member.Queries[0] = query
			if _, err := member.Normalize(); !errors.Is(err, test.want) {
				t.Fatalf("Normalize error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizeGroupedJoinedAggregate(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Symbol: "teams"}
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables: []declaration.Table{
			{ID: "users", Columns: []declaration.Column{{ID: "team-id", Type: declaration.TypeInt64}}},
			{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "region", Type: declaration.TypeString}}},
		},
		Queries: []declaration.Query{{
			ID: "count-by-team-region", Operation: declaration.OperationSelect, Table: users,
			Joins: []declaration.Join{{Kind: declaration.JoinLeft, Table: teams, On: declaration.JoinCondition{
				Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"},
			}}},
			GroupBy:    []declaration.ColumnRef{{Table: teams, Column: "region"}},
			Projection: []declaration.Projection{{Table: teams, Column: "region", Alias: "zone"}, {Aggregate: declaration.AggregateCount, Column: "id", Table: teams, Alias: "total"}, {Aggregate: declaration.AggregateMin, Column: "id", Table: teams, Alias: "minimum"}},
			Results:    []declaration.ResultField{{ID: "zone", Source: "zone", Type: declaration.TypeString, Nullable: true}, {ID: "total", Source: "total", Type: declaration.TypeInt64}, {ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}},
			Having:     declaration.Predicate{Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "zone"}},
			Ordering:   []declaration.Ordering{{Column: "zone", Direction: declaration.OrderingAscending}},
		}},
	}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Queries[0].Joins) != 1 || model.Queries[0].GroupBy[0].Table != teams {
		t.Fatalf("grouped join = %#v", model.Queries[0])
	}
	member.Queries[0].GroupBy[0].Column = "changed"
	if model.Queries[0].GroupBy[0].Column != "region" {
		t.Fatal("normalized grouping changed with authored input")
	}
}

func TestNormalizeUngroupedMultipleAggregates(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "authn",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "region", Type: declaration.TypeString}}}},
		Queries: []declaration.Query{{
			ID: "summary", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			Projection: []declaration.Projection{{Aggregate: declaration.AggregateCount, Alias: "total"}, {Aggregate: declaration.AggregateMin, Column: "id", Alias: "minimum"}, {Aggregate: declaration.AggregateMax, Column: "id", Alias: "maximum"}},
			Results:    []declaration.ResultField{{ID: "total", Source: "total", Type: declaration.TypeInt64}, {ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}, {ID: "maximum", Source: "maximum", Type: declaration.TypeInt64, Nullable: true}},
		}},
	}
	model, err := member.Normalize()
	if err != nil || len(model.Queries[0].Projection) != 3 {
		t.Fatalf("multiple aggregate outputs = %#v, %v", model, err)
	}
	wrongResult := member
	wrongResult.Queries = append([]declaration.Query(nil), member.Queries...)
	wrongResult.Queries[0].Results = append([]declaration.ResultField(nil), member.Queries[0].Results...)
	wrongResult.Queries[0].Results[1].Nullable = false
	if _, err := wrongResult.Normalize(); !errors.Is(err, declaration.ErrInvalidSchema) {
		t.Fatalf("non-null MIN result error = %v", err)
	}
	member.Queries[0].Projection = append(member.Queries[0].Projection, declaration.Projection{Column: "region"})
	member.Queries[0].Results = append(member.Queries[0].Results, declaration.ResultField{ID: "region", Source: "region", Type: declaration.TypeString})
	if _, err := member.Normalize(); !errors.Is(err, declaration.ErrUnsupported) {
		t.Fatalf("mixed ungrouped projection error = %v", err)
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
		Results:    []declaration.ResultField{{ID: "department-id", Source: "department-id", Type: declaration.TypeInt64}},
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
			query.Results = []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}}
		}, declaration.ErrReference},
		{"unintroduced predicate table", func(query *declaration.Query) {
			query.Predicate = declaration.Predicate{Kind: declaration.PredicateIsNull, Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "missing"}, Column: "id"}}
		}, declaration.ErrReference},
		{"predicate has two right operands", func(query *declaration.Query) {
			query.Predicate = declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"}, Value: declaration.Int64(1)}
		}, declaration.ErrInvalidSchema},
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

func TestNormalizeAcceptsNotNullPredicate(t *testing.T) {
	member := declaration.Member[access]{
		Namespace: "accounts",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "email", Type: declaration.TypeString, Nullable: true}}}},
		Queries: []declaration.Query{{
			ID: "active", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			Predicate: declaration.Predicate{Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "email"}},
		}},
	}
	if _, err := member.Normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeRejectsMalformedPredicateFields(t *testing.T) {
	base := declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(1)}
	valid := declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{base, {Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "id"}}}}
	member := declaration.Member[access]{
		Namespace: "accounts",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}},
		Queries:   []declaration.Query{{ID: "find", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}, Predicate: valid}},
	}
	if _, err := member.Normalize(); err != nil {
		t.Fatalf("valid nested predicate: %v", err)
	}
	for _, test := range []struct {
		name      string
		predicate declaration.Predicate
	}{
		{"empty kind with column", declaration.Predicate{Left: declaration.ColumnRef{Column: "id"}}},
		{"empty kind with value", declaration.Predicate{Value: declaration.Int64(1)}},
		{"empty kind with items", declaration.Predicate{Items: []declaration.Predicate{base}}},
		{"comparison with items", declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(1), Items: []declaration.Predicate{base}}},
		{"comparison with unused value", declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Right: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64}}},
		{"null with items", declaration.Predicate{Kind: declaration.PredicateIsNull, Left: declaration.ColumnRef{Column: "id"}, Items: []declaration.Predicate{base}}},
		{"null with unused value", declaration.Predicate{Kind: declaration.PredicateIsNull, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64}}},
		{"boolean with column", declaration.Predicate{Kind: declaration.PredicateAnd, Left: declaration.ColumnRef{Column: "id"}, Items: []declaration.Predicate{base, base}}},
		{"boolean with value", declaration.Predicate{Kind: declaration.PredicateAnd, Value: declaration.Value{Kind: declaration.TypeInt64}, Items: []declaration.Predicate{base, base}}},
		{"nested empty kind", declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{base, {Left: declaration.ColumnRef{Column: "id"}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			member.Queries[0].Predicate = test.predicate
			if _, err := member.Normalize(); !errors.Is(err, declaration.ErrInvalidSchema) {
				t.Fatalf("Normalize error = %v, want %v", err, declaration.ErrInvalidSchema)
			}
		})
	}
}
