//go:build integration

package compiler_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/postgres"
	"github.com/plystra/data/postgres/compiler"
	"github.com/plystra/kernel/configuration"
)

func TestPostgresCheckRejectsWritesAndPreservesNullableSemantics(t *testing.T) {
	ctx, provider := checkDatabase(t)
	member := checkSchemaMember()
	member.Model.Tables[0].Constraints = append(member.Model.Tables[0].Constraints, positiveCheck())
	member.Model.Migrations[0].Snapshot = []declaration.MigrationSchema{{Namespace: "checks", Tables: member.Model.Tables}}
	artifacts, err := compiler.CompileMigrations([]compiler.SchemaMember{member}, "compiler/check", compiler.MigrationHistory{})
	if err != nil {
		t.Fatal(err)
	}
	applyChecks(t, ctx, provider, artifacts)
	if err := checkWrite(t, ctx, provider, member, false, 1, database.Int64Argument(1)); err != nil {
		t.Fatal(err)
	}
	for _, update := range []bool{false, true} {
		if err := checkWrite(t, ctx, provider, member, update, 2, database.Int64Argument(0)); !errors.Is(err, database.ErrBackend) {
			t.Fatalf("invalid write update=%t: %v", update, err)
		}
	}
	// PostgreSQL accepts CHECK's UNKNOWN result. It is not a NOT NULL rule.
	if err := checkWrite(t, ctx, provider, member, false, 3, database.NullArgument()); err != nil {
		t.Fatal("nullable CHECK must accept UNKNOWN", err)
	}
	if err := checkWrite(t, ctx, provider, member, true, 1, database.NullArgument()); err != nil {
		t.Fatal("nullable CHECK update must accept UNKNOWN", err)
	}
	if err := checkWrite(t, ctx, provider, member, true, 1, database.Int64Argument(2)); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresCheckMigrationRollsBackDDLAndHistory(t *testing.T) {
	ctx, provider := checkDatabase(t)
	member := checkSchemaMember()
	initial, err := compiler.CompileMigrations([]compiler.SchemaMember{member}, "compiler/check", compiler.MigrationHistory{})
	if err != nil {
		t.Fatal(err)
	}
	applyChecks(t, ctx, provider, initial)
	if err := checkWrite(t, ctx, provider, member, false, 1, database.Int64Argument(-1)); err != nil {
		t.Fatal(err)
	}
	observed, err := provider.MigrationPlan(ctx, "compiler/check", initial)
	if err != nil {
		t.Fatal(err)
	}
	nonnull := declaration.Constraint{ID: "amount-required", Kind: declaration.ConstraintCheck, Check: declaration.Predicate{
		Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "amount"},
	}}
	member.Model.Migrations = append(member.Model.Migrations, declaration.Migration{ID: "positive", Version: 2,
		Predecessors: []declaration.Ref{{Symbol: "initial"}}, Snapshot: member.Model.Migrations[0].Snapshot,
		Operations: []declaration.MigrationOperation{
			{Kind: declaration.MigrationAddColumn, Table: declaration.Ref{Symbol: "records"}, Column: declaration.Column{ID: "note", Type: declaration.TypeString, Nullable: true}},
			{Kind: declaration.MigrationAddConstraint, Table: declaration.Ref{Symbol: "records"}, Constraint: nonnull},
			{Kind: declaration.MigrationAddConstraint, Table: declaration.Ref{Symbol: "records"}, Constraint: positiveCheck()},
		},
	})
	artifacts, err := compiler.CompileMigrations([]compiler.SchemaMember{member}, "compiler/check", compiler.MigrationHistory{Artifacts: initial, Applied: observed.Applied})
	if err != nil || !reflect.DeepEqual(initial[0], artifacts[0]) {
		t.Fatal("upgrade changed immutable migration 1", err)
	}
	plan, err := provider.MigrationPlan(ctx, "compiler/check", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := provider.ApplyMigrations(ctx, "compiler/check", plan.PlanDigest, artifacts)
	if !errors.Is(err, database.ErrBackend) || len(failed.Applied) != 0 {
		t.Fatalf("existing violating row did not abort migration: %v", err)
	}
	replanned, err := provider.MigrationPlan(ctx, "compiler/check", artifacts)
	if err != nil || replanned.PlanDigest != plan.PlanDigest || !reflect.DeepEqual(replanned.Applied, observed.Applied) || len(replanned.Pending) != 1 {
		t.Fatal("failed migration changed history or pending approval", err)
	}
	// The earlier IS NOT NULL constraint also rolled back: NULL remains valid.
	if err := checkWrite(t, ctx, provider, member, true, 1, database.NullArgument()); err != nil {
		t.Fatal("partial constraint survived rollback", err)
	}
	if err := checkWrite(t, ctx, provider, member, true, 1, database.Int64Argument(1)); err != nil {
		t.Fatal(err)
	}
	// Retry the exact artifact and approval. ADD COLUMN succeeds only if the
	// preceding DDL rolled back too; no repair or rewritten migration is used.
	if _, err := provider.ApplyMigrations(ctx, "compiler/check", plan.PlanDigest, artifacts); err != nil {
		t.Fatal("unchanged migration could not be retried after data repair", err)
	}
	final, err := provider.MigrationPlan(ctx, "compiler/check", artifacts)
	if err != nil || len(final.Applied) != 2 || len(final.Pending) != 0 || final.Applied[0] != observed.Applied[0] {
		t.Fatal("successful upgrade did not retain initial history", err)
	}
	for _, value := range []database.Argument{database.Int64Argument(0), database.NullArgument()} {
		for _, update := range []bool{false, true} {
			if err := checkWrite(t, ctx, provider, member, update, 2, value); !errors.Is(err, database.ErrBackend) {
				t.Fatalf("upgraded CHECK accepted invalid write update=%t: %v", update, err)
			}
		}
	}
}

func applyChecks(t *testing.T, ctx context.Context, provider *postgres.Database, artifacts []compiler.Artifact) {
	t.Helper()
	plan, err := provider.MigrationPlan(ctx, "compiler/check", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyMigrations(ctx, "compiler/check", plan.PlanDigest, artifacts); err != nil {
		t.Fatal(err)
	}
}

// All domain SQL comes from the public typed compiler, then uses Resource.Exec.
func checkWrite(t *testing.T, ctx context.Context, resource database.Resource, member compiler.SchemaMember, update bool, id int64, amount database.Argument) error {
	t.Helper()
	query := declaration.Query{ID: "write", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "records"},
		Parameters: []declaration.Parameter{{ID: "amount", Type: declaration.TypeInt64}, {ID: "id", Type: declaration.TypeInt64}},
		Assignments: []declaration.Assignment{
			{Column: "amount", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "amount"}},
			{Column: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
			{Column: "limit", Value: declaration.Int64(10)}, {Column: "state", Value: declaration.String("active")},
		},
	}
	if update {
		query.Operation = declaration.OperationUpdate
		query.Assignments = query.Assignments[:1]
		// Update id 1 for both valid and invalid writes.
		id = 1
		query.Predicate = declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}}
	}
	member.Model.Queries = []declaration.Query{query}
	queries, err := compiler.CompileQueries([]compiler.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resource.Exec(ctx, database.Plan{Statements: []database.Statement{{
		ID: "write", SQL: queries[0].SQL, Args: []database.Argument{amount, database.Int64Argument(id)}, Affected: database.ExactAffectedRows(1),
	}}})
	return err
}

func checkDatabase(t *testing.T) (context.Context, *postgres.Database) {
	t.Helper()
	if os.Getenv("PLYSTRA_RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("set PLYSTRA_RUN_POSTGRES_INTEGRATION=1 for disposable PostgreSQL tests")
	}
	target := os.Getenv("PLYSTRA_TEST_DATABASE_URL")
	if target == "" {
		t.Fatal("PLYSTRA_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, target)
	if err != nil {
		t.Fatal("connect to disposable PostgreSQL failed")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "plystra_check_test_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{name}.Sanitize()
	// Raw DDL is limited to test-owned database isolation, never domain schema.
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal("create disposable database failed")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+identifier); err != nil {
			t.Error("remove disposable database failed")
		}
	})
	targetURL, err := url.Parse(target)
	if err != nil {
		t.Fatal("invalid disposable PostgreSQL URL")
	}
	targetURL.Path = "/" + name
	query := targetURL.Query()
	query.Del("dbname")
	targetURL.RawQuery = query.Encode()
	t.Setenv("PLYSTRA_CHECK_DATABASE_URL", targetURL.String())
	resolver, err := configuration.NewResolver(configuration.ResolverOptions{MaximumValueBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := configuration.NewEnvironmentReference("PLYSTRA_CHECK_DATABASE_URL")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := resolver.Resolve(ctx, reference)
	if err != nil {
		t.Fatal("resolve disposable PostgreSQL secret failed")
	}
	provider, err := postgres.New(postgres.Config{URL: secret, MaxConnections: 2, HealthTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Stop(context.Background()) })
	return ctx, provider
}
