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

func checkSchemaMember() postgres.SchemaMember {
	table := declaration.Table{ID: "records", Columns: []declaration.Column{
		{ID: "id", Type: declaration.TypeInt64},
		{ID: "amount", Type: declaration.TypeInt64, Nullable: true},
		{ID: "limit", Type: declaration.TypeInt64},
		{ID: "state", Type: declaration.TypeString},
	}, Constraints: []declaration.Constraint{{ID: "records-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}}
	return postgres.SchemaMember{MemberID: "checks.records/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "checks", Tables: []declaration.Table{table},
		Migrations: []declaration.Migration{{ID: "initial", Version: 1,
			Snapshot:   []declaration.MigrationSchema{{Namespace: "checks", Tables: []declaration.Table{table}}},
			Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "records"}}},
		}},
	}}
}

func positiveCheck() declaration.Constraint {
	return declaration.Constraint{ID: "amount-positive", Kind: declaration.ConstraintCheck, Check: declaration.Predicate{
		Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "amount"}, Value: declaration.Int64(0),
	}}
}

func TestCompileCheckSchemaAndMigration(t *testing.T) {
	member := checkSchemaMember()
	check := positiveCheck()
	check.Check = declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{
		check.Check,
		{Kind: declaration.PredicateLessEqual, Left: declaration.ColumnRef{Column: "amount"}, Right: declaration.ColumnRef{Table: declaration.Ref{Symbol: "records"}, Column: "limit"}},
		{Kind: declaration.PredicateOr, Items: []declaration.Predicate{
			{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "state"}, Value: declaration.String("O'Reilly")},
			{Kind: declaration.PredicateIsNull, Left: declaration.ColumnRef{Column: "amount"}},
		}},
	}}
	member.Model.Tables[0].Constraints = append(member.Model.Tables[0].Constraints, check)
	schema, err := postgres.CompileSchema([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	want := `CONSTRAINT "amount-positive" CHECK (("amount" > 0) AND ("amount" <= "limit") AND (("state" = 'O''Reilly') OR ("amount" IS NULL)))`
	if !strings.Contains(schema.SQL, want) || schema.Digest != postgres.DigestSQL(schema.SQL) {
		t.Fatalf("schema CHECK: %s", schema.SQL)
	}
	again, err := postgres.CompileSchema([]postgres.SchemaMember{member})
	if err != nil || !reflect.DeepEqual(schema, again) {
		t.Fatal("CHECK compilation is not deterministic", err)
	}
	member.Model.Migrations = append(member.Model.Migrations, declaration.Migration{ID: "positive", Version: 2,
		Predecessors: []declaration.Ref{{Symbol: "initial"}}, Snapshot: member.Model.Migrations[0].Snapshot,
		Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationAddConstraint, Table: declaration.Ref{Symbol: "records"}, Constraint: check}},
	})
	artifacts, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/check", postgres.MigrationHistory{})
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("migrations: %v", err)
	}
	if !strings.Contains(artifacts[1].SQL, `ALTER TABLE "checks"."records" ADD `+want+";") || strings.Contains(artifacts[0].SQL, "CHECK") {
		t.Fatalf("migration CHECK or frozen initial snapshot: %#v", artifacts)
	}
	// CREATE TABLE can also carry the authored CHECK in its own frozen snapshot.
	member.Model.Migrations = member.Model.Migrations[:1]
	member.Model.Migrations[0].Snapshot = []declaration.MigrationSchema{{Namespace: "checks", Tables: member.Model.Tables}}
	created, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/check", postgres.MigrationHistory{})
	if err != nil || !strings.Contains(created[0].SQL, want) {
		t.Fatal("CREATE TABLE omitted snapshot CHECK", err)
	}
}

func TestCompileCheckPreservesImmutableHistory(t *testing.T) {
	member := checkSchemaMember()
	initial, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/old", postgres.MigrationHistory{})
	if err != nil {
		t.Fatal(err)
	}
	initial[0].SQL += "-- retained old compiler bytes\n"
	initial[0].Definition.ArtifactDigest = postgres.DigestSQL(initial[0].SQL)
	history := postgres.MigrationHistory{Artifacts: initial, Applied: []migration.HistoryEntry{artifactHistory(initial[0])}}
	member.Model.Tables[0].Constraints = append(member.Model.Tables[0].Constraints, positiveCheck())
	member.Model.Migrations = append(member.Model.Migrations, declaration.Migration{ID: "positive", Version: 2,
		Predecessors: []declaration.Ref{{Symbol: "initial"}}, Snapshot: member.Model.Migrations[0].Snapshot,
		Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationAddConstraint, Table: declaration.Ref{Symbol: "records"}, Constraint: positiveCheck()}},
	})
	upgraded, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/new", history)
	if err != nil || !reflect.DeepEqual(upgraded[0], initial[0]) || upgraded[1].Definition.Compiler != "compiler/new" {
		t.Fatal("CHECK upgrade rewrote history", err)
	}
	// A new schema constraint does not retroactively change migration 1's intent.
	fresh, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/new", postgres.MigrationHistory{})
	if err != nil || fresh[0].Definition.LogicalDigest != initial[0].Definition.LogicalDigest || strings.Contains(fresh[0].SQL, "CHECK") {
		t.Fatal("today's CHECK changed the old snapshot", err)
	}
	member.Model.Migrations = member.Model.Migrations[:1]
	member.Model.Migrations[0].Snapshot[0].Tables[0].Constraints = append(member.Model.Migrations[0].Snapshot[0].Tables[0].Constraints, positiveCheck())
	if _, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/new", history); !errors.Is(err, migration.ErrHistoryDrift) {
		t.Fatalf("rewriting historical CHECK accepted: %v", err)
	}
}

func TestCompileCheckRejectsInvalidIntent(t *testing.T) {
	for _, check := range []declaration.Predicate{
		{},
		{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "amount"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "input"}},
		{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "elsewhere"}, Column: "amount"}, Value: declaration.Int64(1)},
		{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "amount"}, Value: declaration.String("1")},
		{Kind: "raw-sql"},
	} {
		member := checkSchemaMember()
		constraint := positiveCheck()
		constraint.Check = check
		member.Model.Tables[0].Constraints = append(member.Model.Tables[0].Constraints, constraint)
		if _, err := postgres.CompileSchema([]postgres.SchemaMember{member}); !errors.Is(err, postgres.ErrSchemaCompilation) {
			t.Fatalf("invalid live CHECK accepted: %v", err)
		}
		member = checkSchemaMember()
		member.Model.Migrations[0].Snapshot[0].Tables[0].Constraints = append(member.Model.Migrations[0].Snapshot[0].Tables[0].Constraints, constraint)
		if _, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/check", postgres.MigrationHistory{}); !errors.Is(err, postgres.ErrSchemaCompilation) {
			t.Fatalf("invalid frozen CHECK accepted: %v", err)
		}
		member = checkSchemaMember()
		member.Model.Migrations[0].Operations = []declaration.MigrationOperation{{Kind: declaration.MigrationAddConstraint, Table: declaration.Ref{Symbol: "records"}, Constraint: constraint}}
		if _, err := postgres.CompileMigrations([]postgres.SchemaMember{member}, "compiler/check", postgres.MigrationHistory{}); !errors.Is(err, postgres.ErrSchemaCompilation) {
			t.Fatalf("invalid add-constraint CHECK accepted: %v", err)
		}
	}
}
