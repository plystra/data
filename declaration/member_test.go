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
		Tables:    []declaration.Table{{ID: "z-table"}, {ID: "a-table"}},
	}
	if _, err := member.Normalize(); err != nil {
		t.Fatal(err)
	}
	if member.Imports[0] != "z.audit/v1" || member.Tables[0].ID != "z-table" {
		t.Fatal("Normalize mutated authored slices")
	}
}
