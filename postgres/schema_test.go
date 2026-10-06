package postgres_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/plystra/data/declaration"
	"github.com/plystra/data/postgres"
)

func TestCompileSchemaIsDeterministicAndLowersRelationalIntent(t *testing.T) {
	audit := postgres.SchemaMember{
		MemberID: "audit.events/v1",
		Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "audit",
			Tables: []declaration.Table{{
				ID:          "events",
				Columns:     []declaration.Column{{ID: "id", Type: declaration.TypeUUID}, {ID: "label", Type: declaration.TypeString, Default: declaration.String("O'Reilly")}},
				Constraints: []declaration.Constraint{{ID: "events-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}},
				Indexes:     []declaration.Index{{ID: "events-label", Columns: []string{"label"}, Unique: true}},
			}},
		},
	}
	auth := postgres.SchemaMember{
		MemberID: "auth.accounts/v1",
		Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "auth",
			Imports:   []string{"audit.events/v1"},
			Tables: []declaration.Table{{
				ID:      "accounts",
				Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}, {ID: "event-id", Type: declaration.TypeUUID}},
				Constraints: []declaration.Constraint{
					{ID: "accounts-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}},
					{ID: "accounts-event", Kind: declaration.ConstraintForeignKey, Columns: []string{"event-id"}, ReferencedTable: declaration.Ref{Member: "audit.events/v1", Symbol: "events"}, ReferencedCols: []string{"id"}},
				},
			}},
		},
	}

	first, err := postgres.CompileSchema([]postgres.SchemaMember{auth, audit})
	if err != nil {
		t.Fatal(err)
	}
	second, err := postgres.CompileSchema([]postgres.SchemaMember{audit, auth})
	if err != nil {
		t.Fatal(err)
	}
	if first.Path != "schema/schema.sql" || first.SQL != second.SQL || first.Digest != second.Digest {
		t.Fatalf("artifacts differ: first=%#v second=%#v", first, second)
	}
	for _, want := range []string{
		`CREATE SCHEMA IF NOT EXISTS "audit";`,
		`CREATE TABLE "audit"."events"`,
		`DEFAULT 'O''Reilly'`,
		`CREATE UNIQUE INDEX "events-label" ON "audit"."events" ("label");`,
		`ALTER TABLE "auth"."accounts" ADD CONSTRAINT "accounts-event" FOREIGN KEY ("event-id") REFERENCES "audit"."events" ("id");`,
	} {
		if !strings.Contains(first.SQL, want) {
			t.Fatalf("SQL missing %q:\n%s", want, first.SQL)
		}
	}
	if auth.Model.Tables[0].Constraints[0].ID != "accounts-pk" {
		t.Fatal("schema compilation mutated authored model")
	}
}

func TestCompileSchemaRejectsCrossResourceForeignKeys(t *testing.T) {
	left := postgres.SchemaMember{
		MemberID: "left.app/v1",
		Resource: "database.primary",
		Model: declaration.Model{
			Namespace: "left",
			Imports:   []string{"right.app/v1"},
			Tables: []declaration.Table{{
				ID:          "items",
				Columns:     []declaration.Column{{ID: "id", Type: declaration.TypeUUID}},
				Constraints: []declaration.Constraint{{ID: "right-ref", Kind: declaration.ConstraintForeignKey, Columns: []string{"id"}, ReferencedTable: declaration.Ref{Member: "right.app/v1", Symbol: "items"}, ReferencedCols: []string{"id"}}},
			}},
		},
	}
	right := postgres.SchemaMember{
		MemberID: "right.app/v1",
		Resource: "database.secondary",
		Model: declaration.Model{
			Namespace: "right",
			Tables:    []declaration.Table{{ID: "items", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}}}},
		},
	}
	_, err := postgres.CompileSchema([]postgres.SchemaMember{left, right})
	if !errors.Is(err, postgres.ErrCrossResourceReference) {
		t.Fatalf("error = %v, want cross-Resource reference", err)
	}
}
