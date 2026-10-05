package migration_test

import (
	"errors"
	"testing"

	"github.com/plystra/data/migration"
)

func TestBuildOrdersDependenciesAndIsInputOrderIndependent(t *testing.T) {
	first := definition("tasks.data/v1", "create", 1, nil)
	second := definition("tasks.data/v1", "seed", 2, []migration.Identity{first.Identity})
	third := definition("tasks.data/v1", "index", 3, []migration.Identity{first.Identity})
	left, err := migration.Build(migration.Request{Target: "postgres:db-1", Backend: "postgres/v1", Compiler: "compiler/v1", Definitions: []migration.Definition{third, second, first}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := migration.Build(migration.Request{Target: "postgres:db-1", Backend: "postgres/v1", Compiler: "compiler/v1", Definitions: []migration.Definition{first, second, third}})
	if err != nil {
		t.Fatal(err)
	}
	if left.PlanDigest != right.PlanDigest {
		t.Fatalf("digest changed with input order: %s != %s", left.PlanDigest, right.PlanDigest)
	}
	if got := []string{left.Pending[0].Identity.Name, left.Pending[1].Identity.Name, left.Pending[2].Identity.Name}; got[0] != "create" || got[1] != "index" || got[2] != "seed" {
		t.Fatalf("pending order = %v", got)
	}
}

func TestBuildRejectsInvalidCatalogAndHistory(t *testing.T) {
	base := definition("tasks.data/v1", "create", 1, nil)
	tests := []struct {
		name    string
		request migration.Request
		want    error
	}{
		{name: "duplicate identity", request: request(base, base), want: migration.ErrDuplicateIdentity},
		{name: "duplicate version", request: request(base, definition("tasks.data/v1", "other", 1, nil)), want: migration.ErrDuplicateVersion},
		{name: "missing predecessor", request: request(definition("tasks.data/v1", "seed", 2, []migration.Identity{{Member: "tasks.data/v1", Name: "missing"}})), want: migration.ErrMissingPredecessor},
		{name: "cycle", request: request(definition("tasks.data/v1", "a", 1, []migration.Identity{{Member: "tasks.data/v1", Name: "b"}}), definition("tasks.data/v1", "b", 2, []migration.Identity{{Member: "tasks.data/v1", Name: "a"}})), want: migration.ErrCycle},
		{name: "history artifact drift", request: migration.Request{Target: "postgres:db-1", Backend: "postgres/v1", Compiler: "compiler/v1", Definitions: []migration.Definition{base}, History: []migration.HistoryEntry{{Identity: base.Identity, Version: 1, LogicalDigest: base.LogicalDigest, ArtifactDigest: "sha256:changed", Compiler: "compiler/v1", Backend: "postgres/v1"}}}, want: migration.ErrHistoryDrift},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := migration.Build(test.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBuildRetainsAppliedPrefixAndCopiesInputs(t *testing.T) {
	first := definition("tasks.data/v1", "create", 1, nil)
	second := definition("tasks.data/v1", "seed", 2, []migration.Identity{first.Identity})
	definitions := []migration.Definition{first, second}
	history := []migration.HistoryEntry{{Identity: first.Identity, Version: first.Version, LogicalDigest: first.LogicalDigest, ArtifactDigest: first.ArtifactDigest, Compiler: first.Compiler, Backend: first.Backend}}
	plan, err := migration.Build(migration.Request{Target: "postgres:db-1", Backend: "postgres/v1", Compiler: "compiler/v1", Definitions: definitions, History: history})
	if err != nil {
		t.Fatal(err)
	}
	definitions[1].Predecessors[0] = migration.Identity{Member: "tasks/v1", Name: "changed"}
	history[0].Identity.Name = "changed"
	if len(plan.Applied) != 1 || plan.Applied[0].Identity.Name != "create" || len(plan.Pending) != 1 || plan.Pending[0].Identity.Name != "seed" {
		t.Fatalf("plan was affected by input mutation: %+v", plan)
	}
}

func definition(member, name string, version uint64, predecessors []migration.Identity) migration.Definition {
	return migration.Definition{Identity: migration.Identity{Member: member, Name: name}, Version: version, Predecessors: predecessors, LogicalDigest: "logical:" + name, ArtifactDigest: "artifact:" + name, Compiler: "compiler/v1", Backend: "postgres/v1"}
}

func request(definitions ...migration.Definition) migration.Request {
	return migration.Request{Target: "postgres:db-1", Backend: "postgres/v1", Compiler: "compiler/v1", Definitions: definitions}
}
