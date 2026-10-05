//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/plystra/data/migration"
	"github.com/plystra/data/postgres"
	"github.com/plystra/kernel/configuration"
)

func TestPostgresMigrationPlanApplyAndReplan(t *testing.T) {
	if os.Getenv("PLYSTRA_RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("set PLYSTRA_RUN_POSTGRES_INTEGRATION=1 for the disposable PostgreSQL acceptance test")
	}
	url := os.Getenv("PLYSTRA_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("PLYSTRA_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resolver, err := configuration.NewResolver(configuration.ResolverOptions{MaximumValueBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLYSTRA_TEST_DATABASE_URL", url)
	reference, err := configuration.NewEnvironmentReference("PLYSTRA_TEST_DATABASE_URL")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := resolver.Resolve(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := postgres.New(postgres.Config{URL: secret, MaxConnections: 2, HealthTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Stop(context.Background()) }()
	sql := "CREATE TABLE IF NOT EXISTS plystra_migration_probe (id integer PRIMARY KEY)"
	definition := migration.Definition{
		Identity:       migration.Identity{Member: "tasks.data/v1", Name: "probe"},
		Version:        1,
		LogicalDigest:  "logical:probe",
		ArtifactDigest: postgres.DigestSQL(sql),
		Compiler:       "compiler/v1",
		Backend:        "postgres/v1",
	}
	artifacts := []postgres.Artifact{{Definition: definition, SQL: sql}}
	plan, err := provider.MigrationPlan(ctx, "compiler/v1", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(plan.Pending))
	}
	if _, err := provider.ApplyMigrations(ctx, "compiler/v1", plan.PlanDigest, artifacts); err != nil {
		t.Fatal(err)
	}
	final, err := provider.MigrationPlan(ctx, "compiler/v1", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Pending) != 0 || len(final.Applied) != 1 {
		t.Fatalf("final plan = applied %d pending %d", len(final.Applied), len(final.Pending))
	}
}
