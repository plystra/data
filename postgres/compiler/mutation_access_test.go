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

func TestCompileMutationAccessProvidersPublicContract(t *testing.T) {
	directory := writeMutationAccessFixture(t)
	for _, race := range []bool{false, true} {
		args := []string{"test", "-mod=mod"}
		if race {
			args = append(args, "-race")
		}
		args = append(args, "./providers/accounts.user/v1", "-run", "^TestGeneratedMutationProviderContract$", "-count=1", "-timeout=60s")
		runAccessFixture(t, directory, args...)
	}
}

func TestCompileMutationAccessProvidersInventory(t *testing.T) {
	member, root := mutationAccessFixture(t)
	first, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
	if err != nil || len(first) != 1 {
		t.Fatal("complete mutation resource rejected", err)
	}
	for left, right := 0, len(root.QueryMethods)-1; left < right; left, right = left+1, right-1 {
		root.QueryMethods[left], root.QueryMethods[right] = root.QueryMethods[right], root.QueryMethods[left]
	}
	second, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
	if err != nil || !reflect.DeepEqual(first, second) || first[0].Digest != postgres.DigestSQL(first[0].Source) {
		t.Fatal("mutation method order changed generated source", err)
	}
	for _, test := range []struct {
		name string
		edit func(*analyzer.Root)
	}{
		{"missing method", func(root *analyzer.Root) { root.QueryMethods = root.QueryMethods[1:] }},
		{"count row type", func(root *analyzer.Root) { root.QueryMethods[0].RowType = "Row" }},
		{"count row package", func(root *analyzer.Root) { root.QueryMethods[0].RowPackage = root.AccessPackage }},
		{"count cardinality", func(root *analyzer.Root) { root.QueryMethods[0].Cardinality = declaration.CardinalityMany }},
		{"count request package", func(root *analyzer.Root) { root.QueryMethods[0].RequestPackage = "example.com/internal/contract" }},
		{"returning row type", func(root *analyzer.Root) { root.QueryMethods[1].RowType = "" }},
		{"returning row package", func(root *analyzer.Root) { root.QueryMethods[1].RowPackage = "example.com/vendor/contract" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			member, root := mutationAccessFixture(t)
			test.edit(&root)
			if got, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root}); !errors.Is(err, postgres.ErrAccessCompilation) || len(got) != 0 {
				t.Fatal("invalid mutation metadata emitted partial source", err)
			}
		})
	}
	// Count-only resources have no row package to import.
	member, root = mutationAccessFixture(t)
	for _, query := range member.Model.Queries {
		if query.Method == root.QueryMethods[0].Name {
			member.Model.Queries = []declaration.Query{query}
			break
		}
	}
	root.QueryMethods = root.QueryMethods[:1]
	root.Model = member.Model
	if got, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root}); err != nil || len(got) != 1 {
		t.Fatal("count-only resource rejected", err)
	}
}

func mutationAccessFixture(t *testing.T) (postgres.SchemaMember, analyzer.Root) {
	t.Helper()
	member, root := accessFixture(t)
	template := member.Model.Queries[0]
	member.Model.Queries, root.QueryMethods = nil, nil
	root.AccessPackage = "example.com/mutationfixture/contract"
	member.Model.Tables[0].Constraints = []declaration.Constraint{{ID: "users-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}
	for _, operation := range []struct {
		kind declaration.OperationKind
		name string
	}{{declaration.OperationInsert, "Insert"}, {declaration.OperationUpdate, "Update"}, {declaration.OperationDelete, "Delete"}} {
		query := declaration.Query{Operation: operation.kind, Table: template.Table, Parameters: template.Parameters}
		request := "Request"
		if operation.kind == declaration.OperationDelete {
			query.Parameters = []declaration.Parameter{{ID: "z-key", GoField: "Key", Type: declaration.TypeInt64}}
			request = "Filter"
		} else {
			for _, column := range member.Model.Tables[0].Columns {
				if operation.kind == declaration.OperationUpdate && column.ID == "id" {
					continue
				}
				for _, result := range template.Results {
					if result.ID != column.ID {
						continue
					}
					field := result.GoField
					if column.Nullable {
						// Leave INSERT optional fields NULL; UPDATE exercises non-null pointers.
						if operation.kind == declaration.OperationInsert {
							continue
						}
						field = field[len("Optional"):]
					}
					for _, parameter := range template.Parameters {
						if parameter.GoField == field {
							query.Assignments = append(query.Assignments, declaration.Assignment{Column: column.ID, Value: declaration.Value{Set: true, Kind: parameter.Type, Parameter: parameter.ID}})
						}
					}
				}
			}
		}
		if operation.kind == declaration.OperationInsert {
			query.Conflict = declaration.Conflict{Constraint: "users-pk", Action: declaration.ConflictDoNothing}
		} else {
			query.Predicate = declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "z-key"}}
		}
		for _, shape := range []struct {
			id, suffix  string
			cardinality declaration.Cardinality
		}{{"count", "Count", declaration.CardinalityNone}, {"many", "Many", declaration.CardinalityMany}, {"one", "One", declaration.CardinalityExactlyOne}, {"optional", "Optional", declaration.CardinalityZeroOrOne}} {
			copy := query
			copy.ID, copy.Method, copy.Cardinality = string(operation.kind)+"-"+shape.id, operation.name+shape.suffix, shape.cardinality
			if operation.kind == declaration.OperationInsert && shape.cardinality == declaration.CardinalityExactlyOne {
				copy.Conflict = declaration.Conflict{}
			}
			method := analyzer.QueryMethod{Name: copy.Method, Cardinality: copy.Cardinality, RequestPackage: root.AccessPackage, RequestType: request}
			if shape.cardinality != declaration.CardinalityNone {
				copy.Returning, copy.Results = template.Projection, template.Results
				method.RowPackage, method.RowType = root.AccessPackage, "Row"
			}
			member.Model.Queries = append(member.Model.Queries, copy)
			root.QueryMethods = append(root.QueryMethods, method)
		}
	}
	member.Model.Queries = append(member.Model.Queries, declaration.Query{ID: "clear", Method: "Clear", Cardinality: declaration.CardinalityNone, Operation: declaration.OperationDelete, Table: template.Table, Predicate: declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(0)}})
	root.QueryMethods = append(root.QueryMethods, analyzer.QueryMethod{Name: "Clear", Cardinality: declaration.CardinalityNone, RequestPackage: root.AccessPackage, RequestType: "Empty"})
	model, err := (declaration.Member[struct{}]{Namespace: member.Model.Namespace, Tables: member.Model.Tables, Queries: member.Model.Queries}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	member.Model, root.Model = model, model
	return member, root
}

func writeMutationAccessFixture(t *testing.T) string {
	t.Helper()
	member, root := mutationAccessFixture(t)
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
	write("go.mod", []byte("module example.com/mutationfixture\n\ngo 1.26\n\nrequire github.com/plystra/data v0.0.0\n\nreplace github.com/plystra/data => "+strconv.Quote(filepath.ToSlash(repository))+"\n"))
	for _, artifact := range artifacts {
		write(artifact.Path, []byte(artifact.Source))
	}
	for _, fixture := range []struct{ source, target string }{{"contract.go.txt", "contract/contract.go"}, {"provider_test.go.txt", "providers/accounts.user/v1/provider_test.go"}, {"provider_integration_test.go.txt", "providers/accounts.user/v1/provider_integration_test.go"}} {
		data, err := os.ReadFile(filepath.Join("testdata", "mutationaccess", fixture.source))
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
