//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/plystra/data/database"
	"github.com/plystra/data/migration"
	"github.com/plystra/data/postgres"
	"github.com/plystra/data/postgres/compiler"
	"github.com/plystra/kernel/configuration"
)

func TestPostgresMigrationPlanApplyAndReplan(t *testing.T) {
	ctx, provider, conn := migrationDatabase(t)
	artifacts := probeArtifacts("CREATE TABLE plystra_migration_probe (id integer PRIMARY KEY)")
	plan, err := provider.MigrationPlan(ctx, "compiler/v1", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(plan.Pending))
	}
	assertRelations(t, ctx, conn, false, "plystra_data_migrations", "plystra_migration_probe")
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
	if _, err := provider.ApplyMigrations(ctx, "compiler/v1", plan.PlanDigest, artifacts); !errors.Is(err, postgres.ErrPlanChanged) {
		t.Fatalf("stale approval = %v", err)
	}
	if _, err := provider.ApplyMigrations(ctx, "compiler/v1", final.PlanDigest, artifacts); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM plystra_data_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("history count = %d, %v", count, err)
	}
}

func TestPostgresMigrationRequiresExactApproval(t *testing.T) {
	ctx, provider, conn := migrationDatabase(t)
	artifacts := probeArtifacts("CREATE TABLE plystra_migration_probe (id integer PRIMARY KEY)")
	for _, test := range []struct {
		digest string
		want   error
	}{{"", postgres.ErrPlanApproval}, {"wrong", postgres.ErrPlanChanged}} {
		if _, err := provider.ApplyMigrations(ctx, "compiler/v1", test.digest, artifacts); !errors.Is(err, test.want) {
			t.Fatalf("apply = %v, want %v", err, test.want)
		}
		assertRelations(t, ctx, conn, false, "plystra_data_migrations", "plystra_migration_probe")
	}
	plan, err := provider.MigrationPlan(ctx, "compiler/v1", artifacts)
	if err != nil || len(plan.Pending) != 1 {
		t.Fatalf("replan = %#v, %v", plan, err)
	}
}

func TestPostgresMigrationArtifactFailureRollsBackAndReplans(t *testing.T) {
	ctx, provider, conn := migrationDatabase(t)
	artifacts := probeArtifacts("CREATE TABLE plystra_migration_probe (id integer PRIMARY KEY)")
	badSQL := "CREATE TABLE plystra_migration_partial (id bigint); SELECT 1 / 0"
	second := probeArtifacts(badSQL)[0]
	second.Definition.Identity.Name = "second"
	second.Definition.Version = 2
	second.Definition.Predecessors = []migration.Identity{artifacts[0].Definition.Identity}
	artifacts = append(artifacts, second)
	plan, err := provider.MigrationPlan(ctx, "compiler/v1", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := provider.ApplyMigrations(ctx, "compiler/v1", plan.PlanDigest, artifacts); !errors.Is(err, database.ErrBackend) || len(result.Applied) != 0 {
		t.Fatalf("apply = %#v, %v", result, err)
	}
	assertRelations(t, ctx, conn, false, "plystra_data_migrations", "plystra_migration_probe", "plystra_migration_partial")
	replanned, err := provider.MigrationPlan(ctx, "compiler/v1", artifacts)
	if err != nil || replanned.PlanDigest != plan.PlanDigest || len(replanned.Pending) != 2 {
		t.Fatalf("replan = %#v, %v", replanned, err)
	}
	artifacts[1].SQL = "CREATE TABLE plystra_migration_partial (id bigint)"
	artifacts[1].Definition.ArtifactDigest = compiler.DigestSQL(artifacts[1].SQL)
	corrected, err := provider.MigrationPlan(ctx, "compiler/v1", artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyMigrations(ctx, "compiler/v1", plan.PlanDigest, artifacts); !errors.Is(err, postgres.ErrPlanChanged) {
		t.Fatalf("changed artifact accepted old approval: %v", err)
	}
	if _, err := provider.ApplyMigrations(ctx, "compiler/v1", corrected.PlanDigest, artifacts); err != nil {
		t.Fatal(err)
	}
	final, err := provider.MigrationPlan(ctx, "compiler/v1", artifacts)
	if err != nil || len(final.Applied) != 2 || len(final.Pending) != 0 {
		t.Fatalf("final = %#v, %v", final, err)
	}
}

func probeArtifacts(sql string) []postgres.Artifact {
	return []postgres.Artifact{{SQL: sql, Definition: migration.Definition{
		Identity: migration.Identity{Member: "tasks.data/v1", Name: "probe"}, Version: 1,
		LogicalDigest: "logical:probe", ArtifactDigest: compiler.DigestSQL(sql), Compiler: "compiler/v1", Backend: "postgres/v1",
	}}}
}

func assertRelations(t *testing.T, ctx context.Context, conn *pgx.Conn, want bool, names ...string) {
	t.Helper()
	for _, name := range names {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil || exists != want {
			t.Fatalf("relation %s exists = %t, %v; want %t", name, exists, err, want)
		}
	}
}

func migrationDatabase(t *testing.T) (context.Context, *postgres.Database, *pgx.Conn) {
	t.Helper()
	if os.Getenv("PLYSTRA_RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("set PLYSTRA_RUN_POSTGRES_INTEGRATION=1 for the disposable PostgreSQL acceptance test")
	}
	url := os.Getenv("PLYSTRA_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("PLYSTRA_TEST_DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "plystra_data_test_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+identifier); err != nil {
			t.Errorf("remove test-owned database: %v", err)
		}
	})
	config, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.Database = name
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	resolver, err := configuration.NewResolver(configuration.ResolverOptions{MaximumValueBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	// Keep credentials in a Secret and give each test an isolated database.
	targetURL, err := neturl.Parse(url)
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	targetURL.Path = "/" + name
	query := targetURL.Query()
	query.Del("dbname")
	targetURL.RawQuery = query.Encode()
	t.Setenv("PLYSTRA_MIGRATION_DATABASE_URL", targetURL.String())
	reference, err := configuration.NewEnvironmentReference("PLYSTRA_MIGRATION_DATABASE_URL")
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
	t.Cleanup(func() { _ = provider.Stop(context.Background()) })
	return ctx, provider, conn
}
