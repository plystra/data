package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/plystra/data/declaration"
	"github.com/plystra/data/migration"
	"github.com/plystra/data/postgres"
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
	artifacts, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "data-compiler/v1")
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
	_, err := postgres.CompileMigrations([]postgres.SchemaMember{left, right}, "data-compiler/v1")
	if !errors.Is(err, postgres.ErrCrossResourceReference) {
		t.Fatalf("error = %v, want cross-Resource reference", err)
	}
}
