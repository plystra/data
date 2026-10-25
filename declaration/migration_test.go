package declaration_test

import (
	"errors"
	"testing"

	"github.com/plystra/data/declaration"
)

func TestMigrationSnapshotNormalizesAndOwnsHistoricalSchema(t *testing.T) {
	table := declaration.Table{ID: "old", Columns: []declaration.Column{{ID: "z", Type: declaration.TypeString}, {ID: "a", Type: declaration.TypeInt64}}, Indexes: []declaration.Index{{ID: "by-z", Columns: []string{"z"}}}}
	member := declaration.Member[struct{}]{Namespace: "today", Migrations: []declaration.Migration{{
		ID: "initial", Version: 1,
		Snapshot:   []declaration.MigrationSchema{{Namespace: "yesterday", Tables: []declaration.Table{table}}},
		Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "old"}}},
	}}}
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if model.Migrations[0].Snapshot[0].Tables[0].Columns[0].ID != "a" || table.Columns[0].ID != "z" {
		t.Fatal("snapshot was not independently normalized")
	}
	digest, _ := member.Digest()
	model.Migrations[0].Snapshot[0].Tables[0].Indexes[0].Columns[0] = "a"
	if table.Indexes[0].Columns[0] != "z" {
		t.Fatal("snapshot index aliases authored input")
	}
	member.Migrations[0].Snapshot[0].Namespace = "changed"
	changed, _ := member.Digest()
	if changed == digest {
		t.Fatal("snapshot omitted from logical digest")
	}
}

func TestMigrationSnapshotRejectsIncompleteOrIgnoredIntent(t *testing.T) {
	tests := []struct {
		name   string
		change func(*declaration.Migration)
	}{
		{"missing snapshot", func(m *declaration.Migration) { m.Snapshot = nil }},
		{"missing table", func(m *declaration.Migration) { m.Operations[0].Table.Symbol = "missing" }},
		{"duplicate schema", func(m *declaration.Migration) { m.Snapshot = append(m.Snapshot, m.Snapshot[0]) }},
		{"invalid namespace", func(m *declaration.Migration) { m.Snapshot[0].Namespace = "Bad" }},
		{"unimported member", func(m *declaration.Migration) { m.Snapshot[0].Member = "other.data/v1" }},
		{"ignored create fields", func(m *declaration.Migration) { m.Operations[0].Column.ID = "ignored" }},
		{"invalid added type", func(m *declaration.Migration) {
			m.Operations[0].Kind = declaration.MigrationAddColumn
			m.Operations[0].Column = declaration.Column{ID: "email", Type: "unknown"}
		}},
		{"duplicate added column", func(m *declaration.Migration) {
			m.Operations[0].Kind = declaration.MigrationAddColumn
			m.Operations[0].Column = declaration.Column{ID: "id", Type: declaration.TypeInt64}
		}},
		{"missing index column", func(m *declaration.Migration) {
			m.Operations[0].Kind = declaration.MigrationCreateIndex
			m.Operations[0].Index = declaration.Index{ID: "by-missing", Columns: []string{"missing"}}
		}},
		{"missing foreign key closure", func(m *declaration.Migration) {
			m.Snapshot[0].Tables[0].Constraints = []declaration.Constraint{{ID: "fk", Kind: declaration.ConstraintForeignKey, Columns: []string{"id"}, ReferencedTable: declaration.Ref{Symbol: "missing"}, ReferencedCols: []string{"id"}}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			member := declaration.Member[struct{}]{Namespace: "accounts", Migrations: []declaration.Migration{{ID: "initial", Version: 1,
				Snapshot:   []declaration.MigrationSchema{{Namespace: "accounts", Tables: []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}}}},
				Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "accounts"}}},
			}}}
			test.change(&member.Migrations[0])
			if _, err := member.Normalize(); err == nil {
				t.Fatal("invalid historical intent accepted")
			}
		})
	}
	member := declaration.Member[struct{}]{Namespace: "accounts", Migrations: []declaration.Migration{{ID: "initial", Version: 1,
		Snapshot:   []declaration.MigrationSchema{{Namespace: "accounts", Tables: []declaration.Table{{ID: "accounts", Columns: make([]declaration.Column, declaration.MaximumNodes)}}}},
		Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "accounts"}}},
	}}}
	if _, err := member.Normalize(); !errors.Is(err, declaration.ErrLimit) {
		t.Fatalf("snapshot nodes bypassed bounds: %v", err)
	}
}
