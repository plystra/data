package compiler_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	postgres "github.com/plystra/data/postgres/compiler"
)

func TestCompileAccessProvidersGeneratedPublicContract(t *testing.T) {
	directory := writeAccessFixture(t)
	for _, race := range []bool{false, true} {
		args := []string{"test", "-mod=mod"}
		if race {
			args = append(args, "-race")
		}
		args = append(args, "./providers/accounts.user/v1", "-run", "^TestGeneratedProviderContract$", "-count=1", "-timeout=60s")
		runAccessFixture(t, directory, args...)
	}
}

func TestCompileAccessProvidersDeterministic(t *testing.T) {
	member, root := accessFixture(t)
	first, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
	if err != nil {
		t.Fatal(err)
	}
	root.QueryMethods[0], root.QueryMethods[2] = root.QueryMethods[2], root.QueryMethods[0]
	second, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
	if err != nil || !reflect.DeepEqual(first, second) || len(first) != 1 || first[0].Digest != postgres.DigestSQL(first[0].Source) {
		t.Fatalf("nondeterministic output: %v", err)
	}
	for _, want := range []string{"//plystra:implements-resource accounts.access/v1", "func New(database db.Resource)", "var _ contract0.Resource", "db.StringArgument(request.Text)", "db.BytesArgument(request.Token)"} {
		if !strings.Contains(first[0].Source, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestCompileAccessProvidersAnalyzedNoAccess(t *testing.T) {
	result, err := analyzer.Analyze(analyzer.Snapshot{Packages: []analyzer.Package{
		{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte("package declaration\ntype NoAccess struct{}\ntype Member[A any] struct{ Namespace string }\n")}}},
		{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\nimport \"github.com/plystra/data/declaration\"\n//plystra:data accounts.user/v1\nvar Schema = declaration.Member[declaration.NoAccess]{Namespace: \"accounts\"}\n")}}},
	}})
	if err != nil || !result.Valid || len(result.Roots) != 1 {
		t.Fatalf("analyze = %#v, %v", result, err)
	}
	root := result.Roots[0]
	artifacts, err := postgres.CompileAccessProviders([]postgres.SchemaMember{{MemberID: root.MemberID, Resource: "database.primary", Model: root.Model}}, result.Roots)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("NoAccess = %#v, %v", artifacts, err)
	}
}

func TestCompileAccessProvidersRejectIncompleteResources(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*postgres.SchemaMember, *analyzer.Root)
	}{
		{"missing method", func(_ *postgres.SchemaMember, root *analyzer.Root) { root.QueryMethods = root.QueryMethods[:2] }},
		{"duplicate method", func(_ *postgres.SchemaMember, root *analyzer.Root) { root.QueryMethods[1] = root.QueryMethods[0] }},
		{"changed cardinality", func(_ *postgres.SchemaMember, root *analyzer.Root) {
			root.QueryMethods[0].Cardinality = declaration.CardinalityMany
		}},
		{"invalid type", func(_ *postgres.SchemaMember, root *analyzer.Root) { root.QueryMethods[0].RowType = "Row;panic()" }},
		{"missing contract", func(_ *postgres.SchemaMember, root *analyzer.Root) { root.AccessID = "" }},
		{"NoAccess with methods", func(_ *postgres.SchemaMember, root *analyzer.Root) { root.MigrationOnly = true }},
		{"unsupported plan method", func(_ *postgres.SchemaMember, root *analyzer.Root) {
			root.PlanMethods = []analyzer.PlanMethod{{Name: "Snapshot"}}
		}},
		{"unsupported callable plan", func(member *postgres.SchemaMember, root *analyzer.Root) {
			query := &member.Model.Queries[0]
			query.Parameters, query.Predicate = nil, declaration.Predicate{}
			plan := declaration.Plan{ID: "snapshot", Method: "Snapshot", Statements: []declaration.PlanStatement{{ID: "read", Query: declaration.Ref{Symbol: query.ID}, Cardinality: declaration.CardinalityExactlyOne}}}
			for _, result := range query.Results {
				plan.Outputs = append(plan.Outputs, declaration.PlanOutput{Statement: "read", Result: result.ID, GoField: result.GoField})
			}
			member.Model.Plans = []declaration.Plan{plan}
			root.Model = member.Model
			root.PlanMethods = []analyzer.PlanMethod{{Name: "Snapshot", RequestPackage: root.AccessPackage, RequestType: "Request", ResultPackage: root.AccessPackage, ResultType: "Row"}}
		}},
		{"changed accepted model", func(member *postgres.SchemaMember, _ *analyzer.Root) { member.Model.Namespace = "changed" }},
		{"unsupported returning mutation", func(member *postgres.SchemaMember, root *analyzer.Root) {
			query := &member.Model.Queries[0]
			query.Operation = declaration.OperationDelete
			query.Returning, query.Projection = query.Projection, nil
			root.Model = member.Model
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			member, root := accessFixture(t)
			test.edit(&member, &root)
			got, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
			if !errors.Is(err, postgres.ErrAccessCompilation) || len(got) != 0 {
				t.Fatalf("partial or accepted resource: %#v, %v", got, err)
			}
		})
	}
	member, root := accessFixture(t)
	for _, roots := range [][]analyzer.Root{nil, {root, root}} {
		if got, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, roots); !errors.Is(err, postgres.ErrAccessCompilation) || len(got) != 0 {
			t.Fatalf("invalid roots accepted: %v", err)
		}
	}
}

func accessFixture(t *testing.T) (postgres.SchemaMember, analyzer.Root) {
	t.Helper()
	columns := []declaration.Column{
		{ID: "id", Type: declaration.TypeInt64}, {ID: "text", Type: declaration.TypeString}, {ID: "active", Type: declaration.TypeBool}, {ID: "token", Type: declaration.TypeBytes}, {ID: "score", Type: declaration.TypeFloat64},
		{ID: "optional-id", Type: declaration.TypeInt64, Nullable: true}, {ID: "optional-text", Type: declaration.TypeString, Nullable: true}, {ID: "optional-active", Type: declaration.TypeBool, Nullable: true}, {ID: "optional-token", Type: declaration.TypeBytes, Nullable: true}, {ID: "optional-score", Type: declaration.TypeFloat64, Nullable: true},
	}
	fields := []string{"Key", "Text", "Active", "Token", "Score", "OptionalKey", "OptionalText", "OptionalActive", "OptionalToken", "OptionalScore"}
	query := declaration.Query{
		Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "users"},
		Parameters: []declaration.Parameter{{ID: "z-key", GoField: "Key", Type: declaration.TypeInt64}, {ID: "a-text", GoField: "Text", Type: declaration.TypeString}, {ID: "b-active", GoField: "Active", Type: declaration.TypeBool}, {ID: "c-token", GoField: "Token", Type: declaration.TypeBytes}, {ID: "d-score", GoField: "Score", Type: declaration.TypeFloat64}},
	}
	query.Predicate = declaration.Predicate{Kind: declaration.PredicateAnd}
	for index, parameter := range query.Parameters {
		query.Predicate.Items = append(query.Predicate.Items, declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: columns[index].ID}, Value: declaration.Value{Kind: parameter.Type, Set: true, Parameter: parameter.ID}})
	}
	// Projection order intentionally disagrees with normalized result-ID order.
	for index := len(columns) - 1; index >= 0; index-- {
		column := columns[index]
		alias := "out-" + column.ID
		query.Projection = append(query.Projection, declaration.Projection{Column: column.ID, Alias: alias})
		query.Results = append(query.Results, declaration.ResultField{ID: column.ID, GoField: fields[index], Source: alias, Type: column.Type, Nullable: column.Nullable})
	}
	var queries []declaration.Query
	var methods []analyzer.QueryMethod
	for _, item := range []struct {
		id, name    string
		cardinality declaration.Cardinality
	}{{"get", "Get", declaration.CardinalityExactlyOne}, {"find", "Find", declaration.CardinalityZeroOrOne}, {"list", "List", declaration.CardinalityMany}} {
		copy := query
		copy.ID, copy.Method, copy.Cardinality = item.id, item.name, item.cardinality
		queries = append(queries, copy)
		methods = append(methods, analyzer.QueryMethod{Name: item.name, Cardinality: item.cardinality, RequestPackage: "example.com/accessfixture/contract", RequestType: "Request", RowPackage: "example.com/accessfixture/contract", RowType: "Row"})
	}
	model, err := (declaration.Member[struct{}]{Namespace: "accounts", Tables: []declaration.Table{{ID: "users", Columns: columns}}, Queries: queries}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	root := analyzer.Root{MemberID: "accounts.user/v1", Model: model, AccessID: "accounts.access/v1", AccessType: "Resource", AccessPackage: "example.com/accessfixture/contract", QueryMethods: methods}
	return postgres.SchemaMember{MemberID: root.MemberID, Model: model, Resource: "database.primary"}, root
}

func writeAccessFixture(t *testing.T) string {
	t.Helper()
	member, root := accessFixture(t)
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
	write("go.mod", []byte("module example.com/accessfixture\n\ngo 1.26\n\nrequire github.com/plystra/data v0.0.0\n\nreplace github.com/plystra/data => "+strconv.Quote(filepath.ToSlash(repository))+"\n"))
	for _, artifact := range artifacts {
		write(artifact.Path, []byte(artifact.Source))
	}
	for _, fixture := range []struct{ source, target string }{{"contract.go.txt", "contract/contract.go"}, {"provider_test.go.txt", "providers/accounts.user/v1/provider_test.go"}, {"provider_integration_test.go.txt", "providers/accounts.user/v1/provider_integration_test.go"}} {
		data, err := os.ReadFile(filepath.Join("testdata", "access", fixture.source))
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

func runAccessFixture(t *testing.T, directory string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated public test: %v\n%s", err, output)
	}
}
