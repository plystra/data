package compiler_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	postgres "github.com/plystra/data/postgres/compiler"
)

func TestCompilePlanAccessProvidersPublicContract(t *testing.T) {
	directory := writePlanAccessFixture(t)
	for _, race := range []bool{false, true} {
		args := []string{"test", "-mod=mod"}
		if race {
			args = append(args, "-race")
		}
		args = append(args, "./providers/accounts.user/v1", "-run", "^TestGeneratedPlanProviderContract$", "-count=1", "-timeout=60s")
		runAccessFixture(t, directory, args...)
	}
}

func TestCompilePlanAccessProvidersDeterminismAndCompleteInventory(t *testing.T) {
	member, root := planAccessFixture(t)
	first, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	root.PlanMethods[0], root.PlanMethods[1] = root.PlanMethods[1], root.PlanMethods[0]
	second, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("method inventory order changed generated source", err)
	}
	for _, edit := range []func(*analyzer.Root){
		func(root *analyzer.Root) { root.PlanMethods = root.PlanMethods[:1] },
		func(root *analyzer.Root) { root.PlanMethods[1] = root.PlanMethods[0] },
		func(root *analyzer.Root) { root.PlanMethods[0].RequestPackage = "example.com/vendor/request" },
		func(root *analyzer.Root) { root.PlanMethods[0].ResultPackage = "example.com/internal/result" },
		func(root *analyzer.Root) { root.PlanMethods[0].ResultType = "Result;panic()" },
	} {
		member, root := planAccessFixture(t)
		edit(&root)
		if got, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root}); !errors.Is(err, postgres.ErrAccessCompilation) || len(got) != 0 {
			t.Fatal("incomplete or inaccessible plan method emitted partial source", err)
		}
	}
}

func planAccessFixture(t *testing.T) (postgres.SchemaMember, analyzer.Root) {
	t.Helper()
	member := atomicRegistrationMember()
	member.Model.Tables[0].Columns = append(member.Model.Tables[0].Columns, declaration.Column{ID: "active", Type: declaration.TypeBool}, declaration.Column{ID: "score", Type: declaration.TypeFloat64}, declaration.Column{ID: "note", Type: declaration.TypeString, Nullable: true})
	for _, extra := range []struct {
		id, field string
		kind      declaration.ScalarType
	}{{"active", "Active", declaration.TypeBool}, {"score", "Score", declaration.TypeFloat64}} {
		member.Model.Queries[0].Parameters = append(member.Model.Queries[0].Parameters, declaration.Parameter{ID: extra.id, Type: extra.kind})
		member.Model.Queries[0].Assignments = append(member.Model.Queries[0].Assignments, declaration.Assignment{Column: extra.id, Value: declaration.Value{Set: true, Kind: extra.kind, Parameter: extra.id}})
		member.Model.Queries[0].Returning = append(member.Model.Queries[0].Returning, declaration.Projection{Column: extra.id})
		member.Model.Queries[0].Results = append(member.Model.Queries[0].Results, declaration.ResultField{ID: extra.id, Source: extra.id, Type: extra.kind})
		member.Model.Plans[0].Parameters = append(member.Model.Plans[0].Parameters, declaration.Parameter{ID: extra.id, GoField: extra.field, Type: extra.kind})
		member.Model.Plans[0].Statements[0].Bindings = append(member.Model.Plans[0].Statements[0].Bindings, declaration.PlanBinding{Parameter: extra.id, FromParameter: extra.id})
		member.Model.Plans[0].Outputs = append(member.Model.Plans[0].Outputs, declaration.PlanOutput{Statement: "account", Result: extra.id, GoField: extra.field})
	}
	member.Model.Queries[0].Returning = append(member.Model.Queries[0].Returning, declaration.Projection{Column: "note"})
	member.Model.Queries[0].Results = append(member.Model.Queries[0].Results, declaration.ResultField{ID: "note", Source: "note", Type: declaration.TypeString, Nullable: true})
	member.Model.Queries[1].Returning = append(member.Model.Queries[1].Returning, declaration.Projection{Column: "digest"})
	member.Model.Queries[1].Results = append(member.Model.Queries[1].Results, declaration.ResultField{ID: "digest", Source: "digest", Type: declaration.TypeBytes})
	member.Model.Plans[0].Outputs = append(member.Model.Plans[0].Outputs, declaration.PlanOutput{Statement: "account", Result: "note", GoField: "Note"}, declaration.PlanOutput{Statement: "session", Result: "digest", GoField: "Digest"}, declaration.PlanOutput{Statement: "account", Result: "email", GoField: "Email"})
	positiveID := declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(0)}
	member.Model.Queries = append(member.Model.Queries, declaration.Query{ID: "clear-sessions", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "sessions"}, Predicate: positiveID}, declaration.Query{ID: "clear-accounts", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "accounts"}, Predicate: positiveID})
	member.Model.Plans = append(member.Model.Plans, declaration.Plan{ID: "clear", Method: "Clear", Statements: []declaration.PlanStatement{
		{ID: "sessions", Query: declaration.Ref{Symbol: "clear-sessions"}, Cardinality: declaration.CardinalityNone, Affected: declaration.AffectedRows{Set: true, Unbounded: true}},
		{ID: "accounts", Query: declaration.Ref{Symbol: "clear-accounts"}, Cardinality: declaration.CardinalityNone, Affected: declaration.AffectedRows{Set: true, Unbounded: true}},
	}})
	for i := range member.Model.Tables {
		member.Model.Tables[i].Constraints = []declaration.Constraint{{ID: "pk-" + member.Model.Tables[i].ID, Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}
	}
	model, err := (declaration.Member[struct{}]{Namespace: member.Model.Namespace, Tables: member.Model.Tables, Queries: member.Model.Queries, Plans: member.Model.Plans}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	member.Model = model
	root := analyzer.Root{MemberID: member.MemberID, Model: model, AccessID: "accounts.access/v1", AccessPackage: "example.com/planfixture/contract", AccessType: "Resource", PlanMethods: []analyzer.PlanMethod{
		{Name: "Register", RequestPackage: "example.com/planfixture/contract", RequestType: "Request", ResultPackage: "example.com/planfixture/contract", ResultType: "Result"},
		{Name: "Clear", RequestPackage: "example.com/planfixture/contract", RequestType: "Empty", ResultPackage: "example.com/planfixture/contract", ResultType: "Empty"},
	}}
	return member, root
}

func writePlanAccessFixture(t *testing.T) string {
	t.Helper()
	member, root := planAccessFixture(t)
	artifacts, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module example.com/planfixture\n\ngo 1.26\n\nrequire github.com/plystra/data v0.0.0\n\nreplace github.com/plystra/data => "+strconv.Quote(filepath.ToSlash(repository))+"\n"))
	for _, artifact := range artifacts {
		write(artifact.Path, []byte(artifact.Source))
	}
	for _, fixture := range []struct{ source, target string }{{"contract.go.txt", "contract/contract.go"}, {"provider_test.go.txt", "providers/accounts.user/v1/provider_test.go"}, {"provider_integration_test.go.txt", "providers/accounts.user/v1/provider_integration_test.go"}} {
		data, err := os.ReadFile(filepath.Join("testdata", "planaccess", fixture.source))
		if err != nil {
			t.Fatal(err)
		}
		write(fixture.target, data)
	}
	schema, err := postgres.CompileSchema([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	write("providers/accounts.user/v1/schema.sql", []byte(schema.SQL))
	return directory
}
