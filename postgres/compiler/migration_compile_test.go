package compiler_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/plystra/data/declaration"
	"github.com/plystra/data/migration"
	postgres "github.com/plystra/data/postgres/compiler"
)

func TestCompileMigrationsProducesPlannerArtifacts(t *testing.T) {
	member := postgres.SchemaMember{
		MemberID: "accounts.data/v1",
		Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "accounts",
			Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}}}},
			Migrations: []declaration.Migration{
				{ID: "create", Version: 1, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "accounts"}}}},
				{ID: "email", Version: 2, Predecessors: []declaration.Ref{{Symbol: "create"}}, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationAddColumn, Table: declaration.Ref{Symbol: "accounts"}, Column: declaration.Column{ID: "email", Type: declaration.TypeString}}}},
				{ID: "index", Version: 3, Predecessors: []declaration.Ref{{Symbol: "email"}}, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateIndex, Table: declaration.Ref{Symbol: "accounts"}, Index: declaration.Index{ID: "accounts-id", Columns: []string{"id"}, Unique: true}}}},
			},
		},
	}
	for i := range member.Model.Migrations {
		member.Model.Migrations[i].Snapshot = []declaration.MigrationSchema{{Namespace: member.Model.Namespace, Tables: member.Model.Tables}}
	}
	artifacts, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "data-compiler/v1", postgres.MigrationHistory{})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 3 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	if artifacts[1].Definition.Predecessors[0].String() != "accounts.data/v1#create" {
		t.Fatalf("predecessors = %#v", artifacts[1].Definition.Predecessors)
	}
	if !strings.Contains(artifacts[0].SQL, `CREATE TABLE "accounts"."accounts"`) || !strings.Contains(artifacts[1].SQL, `ADD COLUMN "email" TEXT NOT NULL`) || !strings.Contains(artifacts[2].SQL, `CREATE UNIQUE INDEX "accounts-id"`) {
		t.Fatalf("SQL artifacts = %#v", artifacts)
	}
	for _, artifact := range artifacts {
		if artifact.Definition.ArtifactDigest != postgres.DigestSQL(artifact.SQL) || artifact.Definition.Compiler != "data-compiler/v1" {
			t.Fatalf("artifact digest metadata = %#v", artifact.Definition)
		}
	}
	definitions := make([]migration.Definition, len(artifacts))
	for index, artifact := range artifacts {
		definitions[index] = artifact.Definition
	}
	plan, err := migration.Build(migration.Request{Target: "postgres:db-1", Backend: "postgres/v1", Compiler: "data-compiler/v1", Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pending) != 3 || plan.Pending[0].Identity.Name != "create" || plan.Pending[2].Identity.Name != "index" {
		t.Fatalf("pending plan = %#v", plan.Pending)
	}
}

func TestCompileMigrationsUsesFrozenSnapshotsAndRetainsHistory(t *testing.T) {
	member := snapshotMember()
	initial, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/old", postgres.MigrationHistory{})
	if err != nil {
		t.Fatal(err)
	}
	// This models an old compiler's different formatting. Retention must use
	// these exact bytes rather than regenerate them with the selected compiler.
	initial[0].SQL += "-- old compiler formatting\n"
	initial[0].Definition.ArtifactDigest = postgres.DigestSQL(initial[0].SQL)
	history := postgres.MigrationHistory{Artifacts: initial, Applied: []migration.HistoryEntry{artifactHistory(initial[0])}}
	member.Model.Namespace = "today"
	member.Model.Tables[0].Columns = append(member.Model.Tables[0].Columns, declaration.Column{ID: "email", Type: declaration.TypeString, Nullable: true})
	member.Model.Migrations = append(member.Model.Migrations, declaration.Migration{
		ID: "email", Version: 2, Predecessors: []declaration.Ref{{Symbol: "create"}},
		Snapshot:   []declaration.MigrationSchema{{Namespace: "accounts", Tables: member.Model.Migrations[0].Snapshot[0].Tables}},
		Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationAddColumn, Table: declaration.Ref{Symbol: "accounts"}, Column: declaration.Column{ID: "email", Type: declaration.TypeString, Nullable: true}}},
	})
	compiled, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/new", history)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(compiled[0], initial[0]) || compiled[1].Definition.Compiler != "compiler/new" || !strings.Contains(compiled[1].SQL, `ALTER TABLE "accounts"."accounts" ADD COLUMN "email" TEXT`) {
		t.Fatalf("retained or pending artifacts changed: %#v", compiled)
	}
	// Even a fresh compile uses each migration's frozen schema, including its
	// namespace, rather than today's table with the later column already added.
	fresh, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/new", postgres.MigrationHistory{})
	if err != nil || strings.Contains(fresh[0].SQL, "email") || strings.Contains(fresh[0].SQL, "today") || fresh[0].Definition.LogicalDigest != initial[0].Definition.LogicalDigest {
		t.Fatalf("snapshot compilation = %#v, %v", fresh, err)
	}
	compiled[1].Definition.Predecessors[0].Name = "changed"
	if member.Model.Migrations[1].Predecessors[0].Symbol != "create" {
		t.Fatal("compiler result aliases source")
	}
}

func TestCompileMigrationsRejectsMissingOrChangedHistoricalEvidence(t *testing.T) {
	base, err := postgres.CompileMigrations([]postgres.SchemaMember{snapshotMember()}, "compiler/old", postgres.MigrationHistory{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*postgres.SchemaMember, *postgres.MigrationHistory)
	}{
		{"missing artifact", func(_ *postgres.SchemaMember, h *postgres.MigrationHistory) { h.Artifacts = nil }},
		{"corrupt bytes", func(_ *postgres.SchemaMember, h *postgres.MigrationHistory) { h.Artifacts[0].SQL += "changed" }},
		{"changed compiler", func(_ *postgres.SchemaMember, h *postgres.MigrationHistory) {
			h.Artifacts[0].Definition.Compiler = "compiler/new"
		}},
		{"missing compiler", func(_ *postgres.SchemaMember, h *postgres.MigrationHistory) { h.Artifacts[0].Definition.Compiler = "" }},
		{"changed version", func(_ *postgres.SchemaMember, h *postgres.MigrationHistory) { h.Artifacts[0].Definition.Version++ }},
		{"changed backend", func(_ *postgres.SchemaMember, h *postgres.MigrationHistory) {
			h.Artifacts[0].Definition.Backend = "other/v1"
		}},
		{"duplicate artifact", func(_ *postgres.SchemaMember, h *postgres.MigrationHistory) {
			h.Artifacts = append(h.Artifacts, h.Artifacts[0])
		}},
		{"deleted source", func(m *postgres.SchemaMember, _ *postgres.MigrationHistory) { m.Model.Migrations = nil }},
		{"changed snapshot", func(m *postgres.SchemaMember, _ *postgres.MigrationHistory) {
			m.Model.Migrations[0].Snapshot[0].Namespace = "changed"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			member := snapshotMember()
			history := postgres.MigrationHistory{Artifacts: append([]postgres.Artifact(nil), base...), Applied: []migration.HistoryEntry{artifactHistory(base[0])}}
			test.change(&member, &history)
			if artifacts, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/new", history); !errors.Is(err, migration.ErrHistoryDrift) || artifacts != nil {
				t.Fatalf("artifacts = %#v, error = %v", artifacts, err)
			}
		})
	}
}

func TestCompileMigrationsFreezesImportedForeignKeyTarget(t *testing.T) {
	member := snapshotMember()
	member.Model.Imports = []string{"identity.data/v1"}
	table := member.Model.Migrations[0].Snapshot[0].Tables[0]
	table.Constraints = []declaration.Constraint{{ID: "fk-identity", Kind: declaration.ConstraintForeignKey, Columns: []string{"id"}, ReferencedTable: declaration.Ref{Member: "identity.data/v1", Symbol: "identities"}, ReferencedCols: []string{"id"}}}
	member.Model.Migrations[0].Snapshot[0].Tables = []declaration.Table{table}
	member.Model.Migrations[0].Snapshot = append(member.Model.Migrations[0].Snapshot, declaration.MigrationSchema{Member: "identity.data/v1", Namespace: "old_identity", Tables: []declaration.Table{{ID: "identities", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}}})
	imported := postgres.SchemaMember{MemberID: "identity.data/v1", Resource: member.Resource, Model: declaration.Model{Namespace: "new_identity"}}
	artifacts, err := postgres.CompileMigrations([]postgres.SchemaMember{member, imported}, "compiler/v1", postgres.MigrationHistory{})
	if err != nil || !strings.Contains(artifacts[0].SQL, `REFERENCES "old_identity"."identities"`) {
		t.Fatalf("imported snapshot = %#v, %v", artifacts, err)
	}
	imported.Resource = "database.other"
	if _, err := postgres.CompileMigrations([]postgres.SchemaMember{member, imported}, "compiler/v1", postgres.MigrationHistory{}); !errors.Is(err, postgres.ErrCrossResourceReference) {
		t.Fatalf("cross-Resource snapshot = %v", err)
	}
}

func TestCompileMigrationsRejectsAmbiguousSnapshotMember(t *testing.T) {
	member := snapshotMember()
	member.Model.Imports = []string{member.MemberID}
	member.Model.Migrations[0].Snapshot = append(member.Model.Migrations[0].Snapshot, declaration.MigrationSchema{
		Member: member.MemberID, Namespace: "ambiguous", Tables: member.Model.Tables,
	})
	if _, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/v1", postgres.MigrationHistory{}); !errors.Is(err, postgres.ErrSchemaCompilation) {
		t.Fatalf("ambiguous snapshot accepted: %v", err)
	}
}

func snapshotMember() postgres.SchemaMember {
	table := declaration.Table{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}
	return postgres.SchemaMember{MemberID: "accounts.data/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts", Tables: []declaration.Table{table},
		Migrations: []declaration.Migration{{ID: "create", Version: 1, Snapshot: []declaration.MigrationSchema{{Namespace: "accounts", Tables: []declaration.Table{table}}}, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "accounts"}}}}},
	}}
}

func artifactHistory(artifact postgres.Artifact) migration.HistoryEntry {
	d := artifact.Definition
	return migration.HistoryEntry{Identity: d.Identity, Version: d.Version, LogicalDigest: d.LogicalDigest, ArtifactDigest: d.ArtifactDigest, Compiler: d.Compiler, Backend: d.Backend}
}

func TestCompileMigrationsRejectsCrossResourcePredecessor(t *testing.T) {
	left := postgres.SchemaMember{
		MemberID: "left.data/v1",
		Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "left",
			Imports:   []string{"right.data/v1"},
			Tables:    []declaration.Table{{ID: "items", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}}}},
			Migrations: []declaration.Migration{{
				ID: "follow", Version: 2, Predecessors: []declaration.Ref{{Member: "right.data/v1", Symbol: "base"}},
				Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "items"}}},
			}},
		},
	}
	right := postgres.SchemaMember{
		MemberID: "right.data/v1",
		Resource: "database.secondary",
		Model: declaration.Model{
			Namespace:  "right",
			Tables:     []declaration.Table{{ID: "items", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}}}},
			Migrations: []declaration.Migration{{ID: "base", Version: 1, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "items"}}}}},
		},
	}
	left.Model.Migrations[0].Snapshot = []declaration.MigrationSchema{{Namespace: left.Model.Namespace, Tables: left.Model.Tables}}
	right.Model.Migrations[0].Snapshot = []declaration.MigrationSchema{{Namespace: right.Model.Namespace, Tables: right.Model.Tables}}
	_, err := postgres.CompileMigrations([]postgres.SchemaMember{left, right}, "data-compiler/v1", postgres.MigrationHistory{})
	if !errors.Is(err, postgres.ErrCrossResourceReference) {
		t.Fatalf("error = %v, want cross-Resource reference", err)
	}
}
