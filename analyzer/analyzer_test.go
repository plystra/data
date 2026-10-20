package analyzer_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
)

const resourceID = "data.database/v1"

func TestAnalyzeUsesOnlyDeclaredStandardExports(t *testing.T) {
	snapshot := analyzer.Snapshot{Packages: []analyzer.Package{{
		ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible,
		Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\nimport \"fmt\"\nvar Text = fmt.Sprint(1)\n")}},
	}}}
	missing, err := analyzer.Analyze(snapshot)
	if err != nil || missing.Valid || !hasCode(missing, analyzer.CodeInvalidSnapshot) {
		t.Fatalf("missing standard export = %#v, %v", missing, err)
	}
	command := exec.Command("go", "list", "-deps", "-export", "-f", "{{if .Export}}{{.ImportPath}}|{{.Export}}{{end}}", "fmt")
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		importPath, name, ok := strings.Cut(line, "|")
		if !ok {
			t.Fatalf("invalid Go export listing %q", line)
		}
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.StandardExports = append(snapshot.StandardExports, analyzer.StandardExport{ImportPath: importPath, Content: content})
	}
	valid, err := analyzer.Analyze(snapshot)
	if err != nil || !valid.Valid || len(valid.Diagnostics) != 0 {
		t.Fatalf("selected standard exports = %#v, %v", valid, err)
	}
	first, err := analyzer.SnapshotDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.StandardExports[0].Content = append(snapshot.StandardExports[0].Content, 0)
	second, err := analyzer.SnapshotDigest(snapshot)
	if err != nil || first == second {
		t.Fatalf("standard export digest = %q, %q, %v", first, second, err)
	}
}

func TestAnalyzeRejectsNonStandardExportInput(t *testing.T) {
	snapshot := analyzer.Snapshot{
		Packages: []analyzer.Package{{
			ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible,
			Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\nimport _ \"example.com/dep\"\n")}},
		}},
		StandardExports: []analyzer.StandardExport{{ImportPath: "example.com/dep", Content: []byte("export")}},
	}
	if _, err := analyzer.Analyze(snapshot); !errors.Is(err, analyzer.ErrInvalidSnapshot) {
		t.Fatalf("non-standard export error = %v", err)
	}
}

func TestAnalyzeUsesTargetArchitectureForUnsafe(t *testing.T) {
	snapshot := analyzer.Snapshot{Packages: []analyzer.Package{{
		ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible,
		Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\nimport \"unsafe\"\nvar _ [4]byte = [unsafe.Sizeof(uintptr(0))]byte{}\n")}},
	}}}
	snapshot.TargetGOARCH = "386"
	on386, err := analyzer.Analyze(snapshot)
	if err != nil || !on386.Valid {
		t.Fatalf("386 type check = %#v, %v", on386, err)
	}
	snapshot.TargetGOARCH = "amd64"
	onAMD64, err := analyzer.Analyze(snapshot)
	if err != nil || onAMD64.Valid || !hasCode(onAMD64, analyzer.CodeTypeCheck) {
		t.Fatalf("amd64 type check = %#v, %v", onAMD64, err)
	}
}

func TestAnalyzeRejectsUnstagedCgoTypes(t *testing.T) {
	snapshot := analyzer.Snapshot{Packages: []analyzer.Package{{
		ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible,
		Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\n/* int value; */\nimport \"C\"\nvar Value = C.value\n")}},
	}}}
	result, err := analyzer.Analyze(snapshot)
	if err != nil || result.Valid || !hasCode(result, analyzer.CodeUnsupportedCapability) {
		t.Fatalf("unstaged cgo type input = %#v, %v", result, err)
	}
}

func TestAnalyzeFindsRootsWithStableProvenance(t *testing.T) {
	snapshot := sourceSnapshot(`package model

import (
	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
)

//plystra:data accounts.user/v1
var Accounts = declaration.Member[database.Resource]{
	Namespace: "accounts",
	Tables: []declaration.Table{{
		ID: "users",
		Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}},
	}},
	Queries: []declaration.Query{{
		ID: "read", Method: "Read", Cardinality: "many", Operation: "select",
		Table: declaration.Ref{Symbol: "users"},
		Projection: []declaration.Projection{{Column: "id"}},
		Results: []declaration.ResultField{{ID: "id", GoField: "ID", Source: "id", Type: "int64"}},
	}},
}

//plystra:data audit.log/v1
var Audit = declaration.Member[declaration.NoAccess]{Namespace: "audit"}

func init() { panic("must not execute") }
`, analyzer.ResourceContract{ImportPath: "github.com/plystra/data/database", TypeName: "Resource", ID: resourceID})

	result, err := analyzer.Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Roots) != 2 || result.Roots[0].MemberID != "accounts.user/v1" || result.Roots[1].MigrationOnly != true {
		t.Fatalf("roots = %#v", result.Roots)
	}
	if result.Roots[0].AccessID != resourceID || result.Roots[0].SourceDigest == "" || result.Digest == "" {
		t.Fatalf("root provenance = %#v", result.Roots[0])
	}
	if len(result.Roots[0].QueryMethods) != 1 || result.Roots[0].QueryMethods[0].Name != "Read" ||
		result.Roots[0].QueryMethods[0].RequestPackage != "github.com/plystra/data/database" ||
		result.Roots[0].QueryMethods[0].RowType != "Row" {
		t.Fatalf("query method shape = %#v", result.Roots[0].QueryMethods)
	}
	if result.Roots[0].DeclarationPos.StartLine == 0 || result.Roots[0].DeclarationPos.Path != "models.go" {
		t.Fatalf("span = %#v", result.Roots[0].DeclarationPos)
	}
}

func TestAnalyzeMatchesAccessMethodNames(t *testing.T) {
	const body = `package model
import (
	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
)
//plystra:data accounts.reader/v1
var Reader = declaration.Member[database.Resource]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}},
	Queries: []declaration.Query{{
		ID: "read-users", Method: "Read", Cardinality: "many", Operation: "select",
		Table: declaration.Ref{Symbol: "users"},
		Projection: []declaration.Projection{{Column: "id"}},
		Results: []declaration.ResultField{{ID: "id", GoField: "ID", Source: "id", Type: "int64"}},
	}},
}`
	resource := analyzer.ResourceContract{ImportPath: "github.com/plystra/data/database", TypeName: "Resource", ID: resourceID}
	for _, test := range []struct {
		name    string
		methods string
		valid   bool
	}{
		{name: "exact", methods: "Read(context.Context, Request) ([]Row, error)", valid: true},
		{name: "extra", methods: "Read(context.Context, Request) ([]Row, error); Write() error"},
		{name: "missing", methods: ""},
		{name: "unexported", methods: "Read(context.Context, Request) ([]Row, error); hidden() error"},
		{name: "lifecycle", methods: "Read(context.Context, Request) ([]Row, error); Close() error"},
		{name: "missing context", methods: "Read(Request) ([]Row, error)"},
		{name: "missing request", methods: "Read(context.Context) ([]Row, error)"},
		{name: "pointer request", methods: "Read(context.Context, *Request) ([]Row, error)"},
		{name: "wrong cardinality", methods: "Read(context.Context, Request) (Row, error)"},
		{name: "wrong row", methods: "Read(context.Context, Request) ([]int64, error)"},
		{name: "wrong error", methods: "Read(context.Context, Request) ([]Row, bool)"},
		{name: "variadic", methods: "Read(context.Context, ...Request) ([]Row, error)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := sourceSnapshot(body, resource)
			snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\nvar _ context.Context\ntype Request struct{}\ntype Row struct{ID int64}\ntype Resource interface { " + test.methods + " }\n")
			result, err := analyzer.Analyze(snapshot)
			if err != nil || result.Valid != test.valid {
				t.Fatalf("method coverage = %#v, %v", result, err)
			}
			if !test.valid && !hasCode(result, analyzer.CodeAccessContractMismatch) {
				t.Fatalf("missing access mismatch: %#v", result.Diagnostics)
			}
		})
	}
	t.Run("embedded", func(t *testing.T) {
		snapshot := sourceSnapshot(body, resource)
		snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\ntype Request struct{}\ntype Row struct{ID int64}\ntype Base interface { Read(context.Context, Request) ([]Row, error) }\ntype Resource interface { Base }\n")
		result, err := analyzer.Analyze(snapshot)
		if err != nil || !result.Valid {
			t.Fatalf("embedded method coverage = %#v, %v", result, err)
		}
	})
	t.Run("wrong row field type", func(t *testing.T) {
		snapshot := sourceSnapshot(body, resource)
		snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\ntype Request struct{}\ntype Row struct{ID string}\ntype Resource interface { Read(context.Context, Request) ([]Row, error) }\n")
		result, err := analyzer.Analyze(snapshot)
		if err != nil || result.Valid || !hasCode(result, analyzer.CodeAccessContractMismatch) {
			t.Fatalf("row field type mismatch = %#v, %v", result, err)
		}
	})
	t.Run("request field mapping", func(t *testing.T) {
		withParameter := strings.Replace(body, `Projection: []declaration.Projection`, `Parameters: []declaration.Parameter{{ID: "id", GoField: "ID", Type: "int64"}},
		Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Parameter: "id"}},
		Projection: []declaration.Projection`, 1)
		for _, test := range []struct {
			name, field string
			valid       bool
		}{
			{name: "exact", field: "ID int64", valid: true},
			{name: "wrong type", field: "ID string"},
			{name: "extra field", field: "ID int64; Other string"},
		} {
			t.Run(test.name, func(t *testing.T) {
				snapshot := sourceSnapshot(withParameter, resource)
				snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\ntype Request struct{" + test.field + "}\ntype Row struct{ID int64}\ntype Resource interface { Read(context.Context, Request) ([]Row, error) }\n")
				result, err := analyzer.Analyze(snapshot)
				if err != nil || result.Valid != test.valid || (!test.valid && !hasCode(result, analyzer.CodeAccessContractMismatch)) {
					t.Fatalf("request mapping = %#v, %v", result, err)
				}
			})
		}
	})
	for _, test := range []struct {
		name, cardinality, result string
	}{
		{name: "exactly one", cardinality: "exactly_one", result: "Row"},
		{name: "zero or one", cardinality: "zero_or_one", result: "*Row"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := strings.Replace(body, `Cardinality: "many"`, `Cardinality: "`+test.cardinality+`"`, 1)
			snapshot := sourceSnapshot(selected, resource)
			snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\ntype Request struct{}\ntype Row struct{ID int64}\ntype Resource interface { Read(context.Context, Request) (" + test.result + ", error) }\n")
			result, err := analyzer.Analyze(snapshot)
			if err != nil || !result.Valid || len(result.Roots) != 1 || result.Roots[0].QueryMethods[0].Cardinality != declaration.Cardinality(test.cardinality) {
				t.Fatalf("cardinality shape = %#v, %v", result, err)
			}
		})
	}
	t.Run("nullable result", func(t *testing.T) {
		selected := strings.Replace(body, `Columns: []declaration.Column{{ID: "id", Type: "int64"}}`, `Columns: []declaration.Column{{ID: "id", Type: "int64", Nullable: true}}`, 1)
		selected = strings.Replace(selected, `GoField: "ID", Source: "id", Type: "int64"`, `GoField: "ID", Source: "id", Type: "int64", Nullable: true`, 1)
		for _, test := range []struct {
			name, field string
			valid       bool
		}{
			{name: "pointer", field: "ID *int64", valid: true},
			{name: "value", field: "ID int64"},
		} {
			t.Run(test.name, func(t *testing.T) {
				snapshot := sourceSnapshot(selected, resource)
				snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\ntype Request struct{}\ntype Row struct{" + test.field + "}\ntype Resource interface { Read(context.Context, Request) ([]Row, error) }\n")
				result, err := analyzer.Analyze(snapshot)
				if err != nil || result.Valid != test.valid || (!test.valid && !hasCode(result, analyzer.CodeAccessContractMismatch)) {
					t.Fatalf("nullable result shape = %#v, %v", result, err)
				}
			})
		}
	})
	t.Run("unsupported access scalar", func(t *testing.T) {
		selected := strings.ReplaceAll(body, `Type: "int64"`, `Type: "uuid"`)
		snapshot := sourceSnapshot(selected, resource)
		snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\ntype Request struct{}\ntype Row struct{ID string}\ntype Resource interface { Read(context.Context, Request) ([]Row, error) }\n")
		result, err := analyzer.Analyze(snapshot)
		if err != nil || result.Valid || !hasCode(result, analyzer.CodeAccessContractMismatch) {
			t.Fatalf("unsupported access scalar = %#v, %v", result, err)
		}
	})
	t.Run("callable plan is not yet executable", func(t *testing.T) {
		selected := strings.Replace(body, "\n}", `
	Plans: []declaration.Plan{{ID: "read-plan", Method: "ReadPlan", Statements: []declaration.PlanStatement{{ID: "read", Query: declaration.Ref{Symbol: "read-users"}, Cardinality: "many"}}}},
}`, 1)
		snapshot := sourceSnapshot(selected, resource)
		snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\ntype Request struct{}\ntype Row struct{ID int64}\ntype Resource interface { Read(context.Context, Request) ([]Row, error); ReadPlan() error }\n")
		result, err := analyzer.Analyze(snapshot)
		if err != nil || result.Valid || !hasCode(result, analyzer.CodeAccessContractMismatch) {
			t.Fatalf("callable plan = %#v, %v", result, err)
		}
	})
}

func TestAnalyzeMatchesReturningMutationMethods(t *testing.T) {
	resource := analyzer.ResourceContract{ImportPath: "github.com/plystra/data/database", TypeName: "Resource", ID: resourceID}
	for _, test := range []struct {
		name, operation, cardinality, result, body string
	}{
		{name: "insert", operation: "insert", cardinality: "exactly_one", result: "Row", body: `Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: "int64", Set: true, Parameter: "id"}}},`},
		{name: "update", operation: "update", cardinality: "zero_or_one", result: "*Row", body: `Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: "int64", Set: true, Parameter: "id"}}}, Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Parameter: "id"}},`},
		{name: "delete", operation: "delete", cardinality: "many", result: "[]Row", body: `Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Parameter: "id"}},`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `package model
import (
	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
)
//plystra:data accounts.writer/v1
var Writer = declaration.Member[database.Resource]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}},
	Queries: []declaration.Query{{
		ID: "write", Method: "Write", Cardinality: "` + test.cardinality + `", Operation: "` + test.operation + `",
		Table: declaration.Ref{Symbol: "users"},
		Parameters: []declaration.Parameter{{ID: "id", GoField: "ID", Type: "int64"}},
		` + test.body + `
		Returning: []declaration.Projection{{Column: "id"}},
		Results: []declaration.ResultField{{ID: "id", GoField: "ID", Source: "id", Type: "int64"}},
	}},
}`
			resourceSource := "package database\nimport \"context\"\ntype Request struct{ID int64}\ntype Row struct{ID int64}\ntype Resource interface { Write(context.Context, Request) (" + test.result + ", error) }\n"
			analyze := func(source, contract string) analyzer.Result {
				t.Helper()
				snapshot := sourceSnapshot(source, resource)
				snapshot.Packages[1].Files[0].Content = []byte(contract)
				result, err := analyzer.Analyze(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			result := analyze(body, resourceSource)
			if !result.Valid || len(result.Roots) != 1 || len(result.Roots[0].QueryMethods) != 1 ||
				result.Roots[0].QueryMethods[0].Cardinality != declaration.Cardinality(test.cardinality) {
				t.Fatalf("returning mutation = %#v", result)
			}
			badRow := analyze(body, strings.Replace(resourceSource, "Row struct{ID int64}", "Row struct{ID string}", 1))
			if badRow.Valid || !hasCode(badRow, analyzer.CodeAccessContractMismatch) {
				t.Fatalf("wrong mutation row = %#v", badRow)
			}
			badRequest := analyze(body, strings.Replace(resourceSource, "Request struct{ID int64}", "Request struct{ID string}", 1))
			if badRequest.Valid || !hasCode(badRequest, analyzer.CodeAccessContractMismatch) {
				t.Fatalf("wrong mutation request = %#v", badRequest)
			}
			countBody := strings.Replace(body, `Cardinality: "`+test.cardinality+`"`, `Cardinality: "none"`, 1)
			countBody = strings.Replace(countBody, `
		Returning: []declaration.Projection{{Column: "id"}},
		Results: []declaration.ResultField{{ID: "id", GoField: "ID", Source: "id", Type: "int64"}},`, "", 1)
			countContract := strings.Replace(resourceSource, test.result+", error)", "uint64, error)", 1)
			countResult := analyze(countBody, countContract)
			if !countResult.Valid || len(countResult.Roots) != 1 || len(countResult.Roots[0].QueryMethods) != 1 ||
				countResult.Roots[0].QueryMethods[0].Cardinality != declaration.CardinalityNone ||
				countResult.Roots[0].QueryMethods[0].RowPackage != "" || countResult.Roots[0].QueryMethods[0].RowType != "" {
				t.Fatalf("affected-row mutation = %#v", countResult)
			}
			wrongCount := analyze(countBody, strings.Replace(countContract, "uint64, error)", "int64, error)", 1))
			if wrongCount.Valid || !hasCode(wrongCount, analyzer.CodeAccessContractMismatch) {
				t.Fatalf("wrong affected-row result = %#v", wrongCount)
			}
		})
	}
}

func TestAnalyzeDecodesAndNormalizesModel(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
	var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{
		{ID: "z-users", Columns: []declaration.Column{{ID: "id", Type: "uuid"}, {ID: "email", Type: "string"}}},
		{ID: "a-users", Columns: []declaration.Column{{ID: "id", Type: "uuid"}}},
	},
	Queries: []declaration.Query{{
		ID: "list-users",
		Operation: "select",
		Table: declaration.Ref{Symbol: "a-users"},
		Projection: []declaration.Projection{{Column: "id", Alias: "user-id"}},
		Results: []declaration.ResultField{{ID: "id", Source: "user-id", Type: "uuid"}},
	}},
	Plans: []declaration.Plan{{
		ID: "list-plan",
		Statements: []declaration.PlanStatement{{ID: "read", Query: declaration.Ref{Symbol: "list-users"}, Cardinality: "many"}},
	}},
	Migrations: []declaration.Migration{{
		ID: "initial",
		Version: 1,
		Operations: []declaration.MigrationOperation{{Kind: "create-table", Table: declaration.Ref{Symbol: "a-users"}}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Roots) != 1 || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
	model := result.Roots[0].Model
	if model.Namespace != "accounts" || len(model.Imports) != 0 {
		t.Fatalf("model identity = %#v", model)
	}
	if len(model.Tables) != 2 || model.Tables[0].ID != "a-users" || model.Tables[1].ID != "z-users" {
		t.Fatalf("normalized tables = %#v", model.Tables)
	}
	if len(model.Tables[1].Columns) != 2 || model.Tables[1].Columns[0].ID != "email" || model.Tables[1].Columns[1].ID != "id" {
		t.Fatalf("normalized columns = %#v", model.Tables[1].Columns)
	}
	if len(model.Queries) != 1 || model.Queries[0].Operation != "select" || model.Queries[0].Table.Symbol != "a-users" || model.Queries[0].Projection[0].Alias != "user-id" {
		t.Fatalf("decoded query = %#v", model.Queries)
	}
	if len(model.Plans) != 1 || model.Plans[0].Statements[0].Query.Symbol != "list-users" {
		t.Fatalf("decoded plan = %#v", model.Plans)
	}
	if len(model.Migrations) != 1 || model.Migrations[0].Version != 1 || model.Migrations[0].Operations[0].Table.Symbol != "a-users" {
		t.Fatalf("decoded migration = %#v", model.Migrations)
	}
}

func TestAnalyzeDecodesTypedMutationQuery(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{
		{ID: "id", Type: "int64"}, {ID: "email", Type: "string"},
	}}},
	Queries: []declaration.Query{{
		ID: "update-email", Operation: "update", Table: declaration.Ref{Symbol: "users"},
		Parameters: []declaration.Parameter{
			{ID: "email", Type: "string"}, {ID: "user-id", Type: "int64"},
		},
		Assignments: []declaration.Assignment{{Column: "email", Value: declaration.Value{Kind: "string", Set: true, Parameter: "email"}}},
		Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Parameter: "user-id"}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Diagnostics) != 0 || len(result.Roots) != 1 {
		t.Fatalf("result = %#v", result)
	}
	query := result.Roots[0].Model.Queries[0]
	if len(query.Parameters) != 2 || query.Parameters[0].ID != "email" || len(query.Assignments) != 1 || query.Assignments[0].Value.Parameter != "email" || query.Predicate.Value.Parameter != "user-id" {
		t.Fatalf("decoded mutation query = %#v", query)
	}
}

func TestAnalyzeDecodesReturningResults(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "email", Type: "string", Nullable: true}}}},
	Queries: []declaration.Query{{
		ID: "update-email", Operation: "update", Table: declaration.Ref{Symbol: "users"},
		Assignments: []declaration.Assignment{{Column: "email", Value: declaration.Value{Kind: "string", Set: true, String: "updated"}}},
		Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
		Returning: []declaration.Projection{{Column: "id", Alias: "user-id"}, {Column: "email"}},
		Results: []declaration.ResultField{{ID: "id", Source: "user-id", Type: "int64"}, {ID: "email", Source: "email", Type: "string", Nullable: true}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Diagnostics) != 0 || len(result.Roots) != 1 {
		t.Fatalf("result = %#v", result)
	}
	query := result.Roots[0].Model.Queries[0]
	if len(query.Returning) != 2 || query.Returning[0].Alias != "user-id" || len(query.Results) != 2 || query.Results[1].Nullable != true {
		t.Fatalf("decoded returning results = %#v", query)
	}
}

func TestAnalyzeRejectsImportedMutationMappingMismatch(t *testing.T) {
	source := `package model
import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"audit.event/v1"},
	Queries: []declaration.Query{{ID: "update-event", Operation: "update", Table: declaration.Ref{Member: "audit.event/v1", Symbol: "events"},
		Assignments: []declaration.Assignment{{Column: "label", Value: declaration.Value{Kind: "string", Set: true, String: "updated"}}, {Column: "revision", Increment: true}},
		Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
		Returning: []declaration.Projection{{Column: "id"}, {Column: "label", Alias: "event-label"}},
		Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}, {ID: "label", Source: "event-label", Type: "string", Nullable: true}},
	}},
}

//plystra:data audit.event/v1
var Events = declaration.Member[declaration.NoAccess]{
	Namespace: "audit", Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "label", Type: "string", Nullable: true}, {ID: "revision", Type: "int64"}}}},
}`
	valid, err := analyzer.Analyze(sourceSnapshot(source))
	if err != nil || !valid.Valid {
		t.Fatalf("valid imported RETURNING = %#v, %v", valid, err)
	}
	for _, test := range []struct {
		name, source string
		code         analyzer.DiagnosticCode
	}{
		{"wrong type", strings.Replace(source, `Source: "id", Type: "int64"`, `Source: "id", Type: "string"`, 1), analyzer.CodeTypeCheck},
		{"wrong nullability", strings.Replace(source, `Source: "event-label", Type: "string", Nullable: true`, `Source: "event-label", Type: "string"`, 1), analyzer.CodeTypeCheck},
		{"missing returning column", strings.Replace(source, `Column: "label", Alias: "event-label"`, `Column: "missing", Alias: "event-label"`, 1), analyzer.CodeUnresolvedReference},
		{"wrong assignment type", strings.Replace(source, `Value{Kind: "string", Set: true, String: "updated"}`, `Value{Kind: "int64", Set: true, Int64: 1}`, 1), analyzer.CodeTypeCheck},
		{"wrong increment type", strings.Replace(source, `Column{{ID: "id", Type: "int64"}, {ID: "label", Type: "string", Nullable: true}, {ID: "revision", Type: "int64"}`, `Column{{ID: "id", Type: "int64"}, {ID: "label", Type: "string", Nullable: true}, {ID: "revision", Type: "string"}`, 1), analyzer.CodeTypeCheck},
		{"nullable increment", strings.Replace(source, `{ID: "revision", Type: "int64"}`, `{ID: "revision", Type: "int64", Nullable: true}`, 1), analyzer.CodeTypeCheck},
		{"missing assignment column", strings.Replace(source, `Assignment{{Column: "label"`, `Assignment{{Column: "missing"`, 1), analyzer.CodeUnresolvedReference},
		{"wrong predicate value type", strings.Replace(source, `Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}}`, `Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "string", Set: true, String: "1"}}`, 1), analyzer.CodeTypeCheck},
		{"wrong predicate column type", strings.Replace(source, `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}}`, `Right: declaration.ColumnRef{Column: "label"}}`, 1), analyzer.CodeTypeCheck},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid, err := analyzer.Analyze(sourceSnapshot(test.source))
			if err != nil || invalid.Valid || !hasCode(invalid, test.code) {
				t.Fatalf("invalid imported RETURNING = %#v, %v", invalid, err)
			}
		})
	}
}

func TestAnalyzeDecodesInsertConflict(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: "int64"}}, Constraints: []declaration.Constraint{{ID: "users-pk", Kind: "primary-key", Columns: []string{"id"}}}}},
	Queries: []declaration.Query{{ID: "insert-user", Operation: "insert", Table: declaration.Ref{Symbol: "users"}, Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}}}, Conflict: declaration.Conflict{Constraint: "users-pk", Action: "do-nothing"}}},
}`))
	if err != nil || !result.Valid || len(result.Diagnostics) != 0 || result.Roots[0].Model.Queries[0].Conflict.Constraint != "users-pk" || result.Roots[0].Model.Queries[0].Conflict.Action != declaration.ConflictDoNothing {
		t.Fatalf("decoded conflict = %#v, %v", result, err)
	}
}

func TestAnalyzeDecodesConflictUpdateAndChecksImportedColumns(t *testing.T) {
	source := `package model
import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"orders.audit/v1"},
	Queries: []declaration.Query{{ID: "insert-event", Operation: "insert", Table: declaration.Ref{Member: "orders.audit/v1", Symbol: "events"},
		Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: "string", Set: true, String: "event"}}, {Column: "label", Value: declaration.Value{Kind: "string", Set: true, String: "new"}}},
		Conflict: declaration.Conflict{Constraint: "events-key", Action: "do-update", UpdateColumns: []string{"label"}},
	}},
}

//plystra:data orders.audit/v1
var Audit = declaration.Member[declaration.NoAccess]{
	Namespace: "orders",
	Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: "string"}, {ID: "label", Type: "string"}}, Constraints: []declaration.Constraint{{ID: "events-key", Kind: "unique", Columns: []string{"id"}}}}},
}`
	result, err := analyzer.Analyze(sourceSnapshot(source))
	if err != nil || !result.Valid || len(result.Diagnostics) != 0 || len(result.Roots) != 2 || result.Roots[0].Model.Queries[0].Conflict.UpdateColumns[0] != "label" {
		t.Fatalf("decoded conflict update = %#v, %v", result, err)
	}
	missing := strings.Replace(source, `{ID: "label", Type: "string"}`, `{ID: "other", Type: "string"}`, 1)
	result, err = analyzer.Analyze(sourceSnapshot(missing))
	if err != nil || result.Valid || !hasCode(result, analyzer.CodeUnresolvedReference) {
		t.Fatalf("missing imported update column = %#v, %v", result, err)
	}
}

func TestAnalyzeDecodesNotNullPredicate(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "email", Type: "string", Nullable: true}}}},
	Queries: []declaration.Query{{ID: "active-users", Operation: "select", Table: declaration.Ref{Symbol: "users"}, Predicate: declaration.Predicate{Kind: "is-not-null", Left: declaration.ColumnRef{Column: "email"}}}},
}`))
	if err != nil || !result.Valid || len(result.Diagnostics) != 0 || result.Roots[0].Model.Queries[0].Predicate.Kind != declaration.PredicateIsNotNull {
		t.Fatalf("decoded not-null predicate = %#v, %v", result, err)
	}
}

func TestAnalyzeDecodesSelectJoins(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{
		{ID: "users", Columns: []declaration.Column{{ID: "team-id", Type: "int64"}}},
		{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: "int64"}}},
	},
	Queries: []declaration.Query{{
		ID: "read", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Joins: []declaration.Join{{Kind: declaration.JoinLeft, Table: declaration.Ref{Symbol: "teams"},
			On: declaration.JoinCondition{
				Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "users"}, Column: "team-id"},
				Right: declaration.ColumnRef{Table: declaration.Ref{Symbol: "teams"}, Column: "id"},
			},
		}},
		Projection: []declaration.Projection{{Table: declaration.Ref{Symbol: "teams"}, Column: "id", Alias: "team-id"}},
		Results: []declaration.ResultField{{ID: "team-id", Source: "team-id", Type: "int64", Nullable: true}},
		Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "teams"}, Column: "id"}, Right: declaration.ColumnRef{Table: declaration.Ref{Symbol: "users"}, Column: "team-id"}},
		Ordering: []declaration.Ordering{{Table: declaration.Ref{Symbol: "teams"}, Column: "id", Direction: "desc"}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Roots) != 1 || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
	joins := result.Roots[0].Model.Queries[0].Joins
	query := result.Roots[0].Model.Queries[0]
	if len(joins) != 1 || joins[0].Kind != "left" || joins[0].On.Left.Column != "team-id" || joins[0].On.Right.Table.Symbol != "teams" || query.Projection[0].Table.Symbol != "teams" || query.Predicate.Left.Table.Symbol != "teams" || query.Predicate.Left.Column != "id" || query.Predicate.Right.Table.Symbol != "users" || query.Predicate.Right.Column != "team-id" || len(query.Ordering) != 1 || query.Ordering[0].Table.Symbol != "teams" || query.Ordering[0].Direction != declaration.OrderingDescending {
		t.Fatalf("decoded joins = %#v", joins)
	}
}

func TestAnalyzeRejectsSelectResultTypeAndJoinNullability(t *testing.T) {
	source := `package model
import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"geo.region/v1"},
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "region-id", Type: "int64"}}}},
	Queries: []declaration.Query{{ID: "read", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Joins: []declaration.Join{{Kind: "left", Table: declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}, On: declaration.JoinCondition{
			Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "users"}, Column: "region-id"},
			Right: declaration.ColumnRef{Table: declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}, Column: "id"},
		}}},
		Projection: []declaration.Projection{{Column: "id"}, {Table: declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}, Column: "name", Alias: "region-name"}},
		Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}, {ID: "name", Source: "region-name", Type: "string", Nullable: true}},
	}},
}

//plystra:data geo.region/v1
var Regions = declaration.Member[declaration.NoAccess]{
	Namespace: "geo", Tables: []declaration.Table{{ID: "regions", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "name", Type: "string"}}}},
}`
	valid, err := analyzer.Analyze(sourceSnapshot(source))
	if err != nil || !valid.Valid {
		t.Fatalf("valid result mapping = %#v, %v", valid, err)
	}
	for _, test := range []struct {
		name, source string
	}{
		{"wrong base type", strings.Replace(source, `Source: "id", Type: "int64"`, `Source: "id", Type: "string"`, 1)},
		{"wrong imported type", strings.Replace(source, `Source: "region-name", Type: "string"`, `Source: "region-name", Type: "int64"`, 1)},
		{"non-null left join", strings.Replace(source, `Source: "region-name", Type: "string", Nullable: true`, `Source: "region-name", Type: "string", Nullable: false`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := analyzer.Analyze(sourceSnapshot(test.source))
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid || !hasCode(result, analyzer.CodeTypeCheck) {
				t.Fatalf("invalid result mapping = %#v", result)
			}
		})
	}
}

func TestAnalyzeDecodesCountProjection(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}},
	Queries: []declaration.Query{{
		ID: "count-users", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Projection: []declaration.Projection{{Aggregate: "count", Alias: "total"}},
		Results: []declaration.ResultField{{ID: "total", Source: "total", Type: "int64"}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
	query := result.Roots[0].Model.Queries[0]
	if len(query.Projection) != 1 || query.Projection[0].Aggregate != declaration.AggregateCount || query.Projection[0].Alias != "total" || query.Results[0].Type != declaration.TypeInt64 {
		t.Fatalf("decoded count query = %#v", query)
	}
}

func TestAnalyzeDecodesMinProjection(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "score", Type: "int64"}}}},
	Queries: []declaration.Query{{
		ID: "minimum-score", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Projection: []declaration.Projection{{Aggregate: "min", Column: "score", Alias: "minimum"}},
		Results: []declaration.ResultField{{ID: "minimum", Source: "minimum", Type: "int64", Nullable: true}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
	query := result.Roots[0].Model.Queries[0]
	if len(query.Projection) != 1 || query.Projection[0].Aggregate != declaration.AggregateMin || query.Projection[0].Column != "score" || !query.Results[0].Nullable {
		t.Fatalf("decoded min query = %#v", query)
	}
}

func TestAnalyzeDecodesMaxProjection(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "score", Type: "int64"}}}},
	Queries: []declaration.Query{{
		ID: "maximum-score", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Projection: []declaration.Projection{{Aggregate: "max", Column: "score", Alias: "maximum"}},
		Results: []declaration.ResultField{{ID: "maximum", Source: "maximum", Type: "int64", Nullable: true}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
	query := result.Roots[0].Model.Queries[0]
	if len(query.Projection) != 1 || query.Projection[0].Aggregate != declaration.AggregateMax || query.Projection[0].Column != "score" || !query.Results[0].Nullable {
		t.Fatalf("decoded max query = %#v", query)
	}
}

func TestAnalyzeRejectsImportedMinMaxNonInt64Input(t *testing.T) {
	source := `package model
import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"metrics.sample/v1"},
	Queries: []declaration.Query{{ID: "minimum", Operation: "select", Table: declaration.Ref{Member: "metrics.sample/v1", Symbol: "samples"},
		Projection: []declaration.Projection{{Aggregate: "min", Column: "value", Alias: "minimum"}},
		Results: []declaration.ResultField{{ID: "minimum", Source: "minimum", Type: "int64", Nullable: true}},
	}},
}

//plystra:data metrics.sample/v1
var Samples = declaration.Member[declaration.NoAccess]{
	Namespace: "metrics", Tables: []declaration.Table{{ID: "samples", Columns: []declaration.Column{{ID: "value", Type: "int64"}}}},
}`
	for _, aggregate := range []string{"min", "max"} {
		t.Run(aggregate, func(t *testing.T) {
			querySource := strings.Replace(source, `Aggregate: "min"`, `Aggregate: "`+aggregate+`"`, 1)
			valid, err := analyzer.Analyze(sourceSnapshot(querySource))
			if err != nil || !valid.Valid {
				t.Fatalf("valid imported aggregate = %#v, %v", valid, err)
			}
			invalidSource := strings.Replace(querySource, `ID: "value", Type: "int64"`, `ID: "value", Type: "string"`, 1)
			invalid, err := analyzer.Analyze(sourceSnapshot(invalidSource))
			if err != nil || invalid.Valid || !hasCode(invalid, analyzer.CodeTypeCheck) {
				t.Fatalf("invalid imported aggregate = %#v, %v", invalid, err)
			}
		})
	}
}

func TestAnalyzeRejectsImportedSumNonFloat64Input(t *testing.T) {
	source := `package model
import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"metrics.sample/v1"},
	Queries: []declaration.Query{{ID: "total", Operation: "select", Table: declaration.Ref{Member: "metrics.sample/v1", Symbol: "samples"},
		Projection: []declaration.Projection{{Aggregate: "sum", Column: "value", Alias: "total"}},
		Results: []declaration.ResultField{{ID: "total", Source: "total", Type: "float64", Nullable: true}},
	}},
}

//plystra:data metrics.sample/v1
var Samples = declaration.Member[declaration.NoAccess]{
	Namespace: "metrics", Tables: []declaration.Table{{ID: "samples", Columns: []declaration.Column{{ID: "value", Type: "float64"}}}},
}`
	valid, err := analyzer.Analyze(sourceSnapshot(source))
	if err != nil || !valid.Valid {
		t.Fatalf("valid imported SUM = %#v, %v", valid, err)
	}
	invalidSource := strings.Replace(source, `ID: "value", Type: "float64"`, `ID: "value", Type: "int64"`, 1)
	invalid, err := analyzer.Analyze(sourceSnapshot(invalidSource))
	if err != nil || invalid.Valid || !hasCode(invalid, analyzer.CodeTypeCheck) {
		t.Fatalf("invalid imported SUM = %#v, %v", invalid, err)
	}
}

func TestAnalyzeDecodesGroupedAggregate(t *testing.T) {
	source := `package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "region", Type: "string"}, {ID: "tier", Type: "string"}, {ID: "id", Type: "int64"}}}},
	Queries: []declaration.Query{{
		ID: "count-by-region", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		GroupBy: []declaration.ColumnRef{{Column: "region"}, {Column: "tier"}},
		Projection: []declaration.Projection{{Column: "region"}, {Column: "tier"}, {Aggregate: "count", Column: "id", Alias: "total"}},
		Having: declaration.Predicate{Kind: "greater", Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
		Ordering: []declaration.Ordering{{Column: "total", Direction: "desc"}, {Column: "region", Direction: "asc"}},
		Results: []declaration.ResultField{{ID: "region", Source: "region", Type: "string"}, {ID: "tier", Source: "tier", Type: "string"}, {ID: "total", Source: "total", Type: "int64"}},
	}},
}`
	result, err := analyzer.Analyze(sourceSnapshot(source))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
	query := result.Roots[0].Model.Queries[0]
	if len(query.GroupBy) != 2 || query.GroupBy[0].Column != "region" || query.GroupBy[1].Column != "tier" || len(query.Projection) != 3 || query.Projection[2].Aggregate != declaration.AggregateCount || query.Having.Left.Column != "total" || len(query.Ordering) != 2 || query.Ordering[0].Column != "total" {
		t.Fatalf("decoded grouped query = %#v", query)
	}
	for _, test := range []struct {
		name, source string
		code         analyzer.DiagnosticCode
	}{
		{"missing output", strings.Replace(source, `Column: "total", Direction: "desc"`, `Column: "missing", Direction: "desc"`, 1), analyzer.CodeUnresolvedReference},
		{"qualified output", strings.Replace(source, `Column: "total", Direction: "desc"`, `Table: declaration.Ref{Symbol: "users"}, Column: "total", Direction: "desc"`, 1), analyzer.CodeInvalidRootShape},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid, err := analyzer.Analyze(sourceSnapshot(test.source))
			if err != nil || invalid.Valid || !hasCode(invalid, test.code) {
				t.Fatalf("invalid grouped ordering = %#v, %v", invalid, err)
			}
		})
	}
}

func TestAnalyzeRejectsHavingTypeMismatch(t *testing.T) {
	source := `package model
import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "region", Type: "string"}, {ID: "id", Type: "int64"}}}},
	Queries: []declaration.Query{{ID: "count-by-region", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		GroupBy: []declaration.ColumnRef{{Column: "region"}},
		Projection: []declaration.Projection{{Column: "region"}, {Aggregate: "count", Column: "id", Alias: "total"}},
		Having: declaration.Predicate{Kind: "and", Items: []declaration.Predicate{
			{Kind: "greater", Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
			{Kind: "not-equal", Left: declaration.ColumnRef{Column: "region"}, Value: declaration.Value{Kind: "string", Set: true, String: "west"}},
		}},
		Results: []declaration.ResultField{{ID: "region", Source: "region", Type: "string"}, {ID: "total", Source: "total", Type: "int64"}},
	}},
}`
	valid, err := analyzer.Analyze(sourceSnapshot(source))
	if err != nil || !valid.Valid {
		t.Fatalf("valid HAVING = %#v, %v", valid, err)
	}
	for _, test := range []struct{ name, source string }{
		{"wrong value type", strings.Replace(source, `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}`, `Value: declaration.Value{Kind: "string", Set: true, String: "1"}`, 1)},
		{"wrong output type", strings.Replace(source, `Value: declaration.Value{Kind: "string", Set: true, String: "west"}`, `Right: declaration.ColumnRef{Column: "total"}`, 1)},
		{"wrong parameter type", strings.Replace(strings.Replace(source, `GroupBy: []declaration.ColumnRef`, `Parameters: []declaration.Parameter{{ID: "threshold", Type: "string"}}, GroupBy: []declaration.ColumnRef`, 1), `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}`, `Value: declaration.Value{Kind: "string", Set: true, Parameter: "threshold"}`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid, err := analyzer.Analyze(sourceSnapshot(test.source))
			if err != nil || invalid.Valid || !hasCode(invalid, analyzer.CodeTypeCheck) {
				t.Fatalf("invalid HAVING = %#v, %v", invalid, err)
			}
		})
	}
}

func TestAnalyzeGroupedImportedLeftJoin(t *testing.T) {
	source := `package model
import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"geo.region/v1"},
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "region-id", Type: "int64"}}}},
	Queries: []declaration.Query{{ID: "count-by-region", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Joins: []declaration.Join{{Kind: "left", Table: declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}, On: declaration.JoinCondition{
			Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "users"}, Column: "region-id"},
			Right: declaration.ColumnRef{Table: declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}, Column: "id"},
		}}},
		GroupBy: []declaration.ColumnRef{{Table: declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}, Column: "name"}},
		Projection: []declaration.Projection{{Table: declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}, Column: "name", Alias: "region-name"}, {Aggregate: "count", Alias: "total"}, {Aggregate: "max", Table: declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}, Column: "score", Alias: "maximum"}},
		Having: declaration.Predicate{Kind: "is-not-null", Left: declaration.ColumnRef{Column: "region-name"}},
		Ordering: []declaration.Ordering{{Column: "region-name", Direction: "asc"}},
		HasLimit: true, Limit: 2, HasOffset: true, Offset: 1,
		Results: []declaration.ResultField{{ID: "name", Source: "region-name", Type: "string", Nullable: true}, {ID: "total", Source: "total", Type: "int64"}, {ID: "maximum", Source: "maximum", Type: "int64", Nullable: true}},
	}},
}

//plystra:data geo.region/v1
var Regions = declaration.Member[declaration.NoAccess]{
	Namespace: "geo", Tables: []declaration.Table{{ID: "regions", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "name", Type: "string"}, {ID: "score", Type: "int64"}}}},
}`
	valid, err := analyzer.Analyze(sourceSnapshot(source))
	if err != nil || !valid.Valid || len(valid.Roots) != 2 || len(valid.Roots[0].Model.Queries) != 1 || !valid.Roots[0].Model.Queries[0].HasLimit || valid.Roots[0].Model.Queries[0].Offset != 1 || len(valid.Roots[0].Model.Queries[0].Projection) != 3 {
		t.Fatalf("grouped imported join = %#v, %v", valid, err)
	}
	for _, test := range []struct {
		name, source string
		code         analyzer.DiagnosticCode
	}{
		{"non-null left result", strings.Replace(source, `Source: "region-name", Type: "string", Nullable: true`, `Source: "region-name", Type: "string"`, 1), analyzer.CodeTypeCheck},
		{"missing grouping column", strings.Replace(source, `Column: "name"}},`, `Column: "missing"}},`, 1), analyzer.CodeUnresolvedReference},
		{"non-int64 max input", strings.Replace(source, `Column: "score", Alias: "maximum"`, `Column: "name", Alias: "maximum"`, 1), analyzer.CodeTypeCheck},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid, err := analyzer.Analyze(sourceSnapshot(test.source))
			if err != nil || invalid.Valid || !hasCode(invalid, test.code) {
				t.Fatalf("invalid grouped imported join = %#v, %v", invalid, err)
			}
		})
	}
}

func TestAnalyzeRejectsInvalidJoinReferences(t *testing.T) {
	source := `package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"teams.member/v1"},
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "team-id", Type: "int64"}, {ID: "code", Type: "string"}}}},
	Queries: []declaration.Query{{ID: "read", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Joins: []declaration.Join{{Kind: "inner", Table: declaration.Ref{Member: "teams.member/v1", Symbol: "teams"}, On: declaration.JoinCondition{
			Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "users"}, Column: "team-id"},
			Right: declaration.ColumnRef{Table: declaration.Ref{Member: "teams.member/v1", Symbol: "teams"}, Column: "id"},
		}}},
		Predicate: declaration.Predicate{Kind: "and", Items: []declaration.Predicate{
			{Kind: "equal", Left: declaration.ColumnRef{Table: declaration.Ref{Member: "teams.member/v1", Symbol: "teams"}, Column: "id"}, Right: declaration.ColumnRef{Column: "team-id"}},
			{Kind: "or", Items: []declaration.Predicate{
				{Kind: "equal", Left: declaration.ColumnRef{Column: "team-id"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
				{Kind: "is-not-null", Left: declaration.ColumnRef{Column: "code"}},
			}},
		}},
	}},
}

//plystra:data teams.member/v1
var Teams = declaration.Member[declaration.NoAccess]{Namespace: "teams", Tables: []declaration.Table{{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}}}
`
	valid, err := analyzer.Analyze(sourceSnapshot(source))
	if err != nil || !valid.Valid {
		t.Fatalf("valid joined predicate = %#v, %v", valid, err)
	}
	for _, test := range []struct {
		name, source string
		code         analyzer.DiagnosticCode
	}{
		{"missing target table", strings.ReplaceAll(source, `Symbol: "teams"`, `Symbol: "missing"`), analyzer.CodeUnresolvedReference},
		{"wrong target kind", strings.Replace(source,
			`Tables: []declaration.Table{{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}}}`,
			`Tables: []declaration.Table{{ID: "other", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}}, Queries: []declaration.Query{{ID: "teams", Operation: "select", Table: declaration.Ref{Symbol: "other"}}}}`, 1), analyzer.CodeReferenceKindMismatch},
		{"missing join column", strings.Replace(source, `Column: "id"`, `Column: "missing"`, 1), analyzer.CodeUnresolvedReference},
		{"mismatched column type", strings.Replace(source, `ID: "id", Type: "int64"`, `ID: "id", Type: "string"`, 1), analyzer.CodeTypeCheck},
		{"mismatched joined predicate columns", strings.Replace(source, `Right: declaration.ColumnRef{Column: "team-id"}`, `Right: declaration.ColumnRef{Column: "code"}`, 1), analyzer.CodeTypeCheck},
		{"mismatched nested predicate value", strings.Replace(source, `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}`, `Value: declaration.Value{Kind: "string", Set: true, String: "1"}`, 1), analyzer.CodeTypeCheck},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := analyzer.Analyze(sourceSnapshot(test.source))
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid || !hasCode(result, test.code) {
				t.Fatalf("result = %#v, want %s", result, test.code)
			}
		})
	}
}

func TestAnalyzeRejectsIncompleteReturningResults(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users"}},
	Queries: []declaration.Query{{
		ID: "delete-user", Operation: "delete", Table: declaration.Ref{Symbol: "users"},
		Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
		Returning: []declaration.Projection{{Column: "id"}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || len(result.Diagnostics) == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestAnalyzeResolvesImportedReferences(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Imports: []string{"orders.audit/v1"},
	Queries: []declaration.Query{
		{ID: "audit-events", Operation: "select", Table: declaration.Ref{Member: "orders.audit/v1", Symbol: "events"}, Projection: []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "string"}}},
		{ID: "insert-event", Operation: "insert", Table: declaration.Ref{Member: "orders.audit/v1", Symbol: "events"}, Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: "string", Set: true, String: "event"}}}, Conflict: declaration.Conflict{Constraint: "events-key", Action: "do-nothing"}},
	},
}

//plystra:data orders.audit/v1
var Audit = declaration.Member[declaration.NoAccess]{
	Namespace: "orders",
	Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: "string"}}, Constraints: []declaration.Constraint{{ID: "events-key", Kind: "unique", Columns: []string{"id"}}}}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Roots) != 2 || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestAnalyzeRejectsMissingImportedMember(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Imports: []string{"orders.audit/v1"},
	Queries: []declaration.Query{{ID: "events", Operation: "select", Table: declaration.Ref{Member: "orders.audit/v1", Symbol: "events"}}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || !hasCode(result, analyzer.CodeUnresolvedReference) {
		t.Fatalf("result = %#v", result)
	}
}

func TestAnalyzeRejectsReferenceKindMismatchAndImportCycle(t *testing.T) {
	mismatch, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts", Imports: []string{"orders.audit/v1"}, Queries: []declaration.Query{{ID: "read", Operation: "select", Table: declaration.Ref{Member: "orders.audit/v1", Symbol: "find"}}}}

//plystra:data orders.audit/v1
var Audit = declaration.Member[declaration.NoAccess]{Namespace: "orders", Queries: []declaration.Query{{ID: "find", Operation: "select", Table: declaration.Ref{Symbol: "local"}}}, Tables: []declaration.Table{{ID: "local"}}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if mismatch.Valid || !hasCode(mismatch, analyzer.CodeReferenceKindMismatch) {
		t.Fatalf("mismatch result = %#v", mismatch)
	}

	cycle, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts", Imports: []string{"orders.audit/v1"}}

//plystra:data orders.audit/v1
var Audit = declaration.Member[declaration.NoAccess]{Namespace: "orders", Imports: []string{"accounts.user/v1"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if cycle.Valid || !hasCode(cycle, analyzer.CodeImportCycle) {
		t.Fatalf("cycle result = %#v", cycle)
	}
}

func TestAnalyzeRejectsInvalidNormalizedReferences(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{
		ID: "users",
		Columns: []declaration.Column{{ID: "id", Type: "uuid"}},
		Indexes: []declaration.Index{{ID: "by-missing", Columns: []string{"missing"}}},
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || len(result.Roots) != 0 || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != analyzer.CodeUnresolvedReference {
		t.Fatalf("result = %#v", result)
	}
}

func TestAnalyzeIsDeterministicAcrossInputOrder(t *testing.T) {
	first := sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.zed/v1
var Zed = declaration.Member[declaration.NoAccess]{Namespace: "zed"}
`, analyzer.ResourceContract{})
	second := sourceSnapshotFile("other.go", `package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.alpha/v1
var Alpha = declaration.Member[declaration.NoAccess]{Namespace: "alpha"}
	`, analyzer.ResourceContract{})
	first.Packages[2].Files = append(first.Packages[2].Files, second.Packages[2].Files...)
	reordered := first
	reordered.Packages = append([]analyzer.Package(nil), first.Packages...)
	reordered.Packages[2].Files = append([]analyzer.SourceFile(nil), first.Packages[2].Files...)
	for left, right := 0, len(reordered.Packages[2].Files)-1; left < right; left, right = left+1, right-1 {
		reordered.Packages[2].Files[left], reordered.Packages[2].Files[right] = reordered.Packages[2].Files[right], reordered.Packages[2].Files[left]
	}

	left, err := analyzer.Analyze(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := analyzer.Analyze(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if left.Digest != right.Digest {
		t.Fatalf("digest changed with input order: %s != %s", left.Digest, right.Digest)
	}
}

func TestAnalyzeSupportPackageCannotContributeRoots(t *testing.T) {
	snapshot := sourceSnapshot(`package model

import (
	_ "example.com/support"
	"github.com/plystra/data/declaration"
)

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts"}
`)
	snapshot.Packages = append(snapshot.Packages, analyzer.Package{
		ImportPath: "example.com/support", RootEligibility: analyzer.RootSupport,
		Files: []analyzer.SourceFile{{Path: "support.go", Content: []byte(`package support

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Injected = declaration.Member[declaration.NoAccess]{Namespace: "injected"}

func init() { panic("must not execute") }
`)}},
	})
	snapshot.Limits.MaxRoots = 1
	result, err := analyzer.Analyze(snapshot)
	if err != nil || !result.Valid || len(result.Diagnostics) != 0 || len(result.Roots) != 1 || result.Roots[0].PackagePath != "example.com/model" {
		t.Fatalf("support package injected a root: %#v, %v", result, err)
	}
	firstDigest, err := analyzer.SnapshotDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Packages[len(snapshot.Packages)-1].RootEligibility = analyzer.RootEligible
	secondDigest, err := analyzer.SnapshotDigest(snapshot)
	if err != nil || firstDigest == secondDigest {
		t.Fatalf("eligibility did not change snapshot digest: %q, %q, %v", firstDigest, secondDigest, err)
	}
	result, err = analyzer.Analyze(snapshot)
	if err != nil || result.Valid || !hasCode(result, analyzer.CodeDuplicateSymbol) {
		t.Fatalf("eligible duplicate root = %#v, %v", result, err)
	}
}

func TestAnalyzeRejectsMissingOrUnknownRootEligibility(t *testing.T) {
	for _, eligibility := range []analyzer.RootEligibility{"", "unknown"} {
		snapshot := sourceSnapshot("package model")
		snapshot.Packages[2].RootEligibility = eligibility
		if _, err := analyzer.SnapshotDigest(snapshot); !errors.Is(err, analyzer.ErrInvalidSnapshot) {
			t.Fatalf("eligibility %q digest error = %v", eligibility, err)
		}
		if _, err := analyzer.Analyze(snapshot); !errors.Is(err, analyzer.ErrInvalidSnapshot) {
			t.Fatalf("eligibility %q analysis error = %v", eligibility, err)
		}
	}
}

func TestAnalyzeRejectsClosedGrammarViolations(t *testing.T) {
	tests := []struct {
		name string
		code string
		want analyzer.DiagnosticCode
	}{
		{
			name: "call",
			code: `var Accounts = declaration.Member[declaration.NoAccess]{Namespace: stringValue()}`,
			want: analyzer.CodeUnsupportedExpression,
		},
		{
			name: "unkeyed",
			code: `var Accounts = declaration.Member[declaration.NoAccess]{"accounts", nil, nil, nil, nil, nil, declaration.NoAccess{}}`,
			want: analyzer.CodeUnsupportedExpression,
		},
		{
			name: "variable",
			code: `var Accounts = declaration.Member[declaration.NoAccess]{Namespace: value}`,
			want: analyzer.CodeUnsupportedExpression,
		},
		{
			name: "unknown-access",
			code: `var Accounts = declaration.Member[customAccess]{Namespace: "accounts"}`,
			want: analyzer.CodeAccessContractMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := analyzer.Analyze(sourceSnapshot("package model\n\nimport \"github.com/plystra/data/declaration\"\n\ntype customAccess interface{}\nvar value = \"accounts\"\nfunc stringValue() string { return value }\n\n//plystra:data accounts.user/v1\n"+test.code+"\n", analyzer.ResourceContract{}))
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid || len(result.Diagnostics) == 0 || result.Diagnostics[0].Code != test.want {
				t.Fatalf("result = %#v, want %s", result, test.want)
			}
		})
	}
}

func TestAnalyzeRejectsDuplicateAndReservedRoots(t *testing.T) {
	duplicate := sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts"}

//plystra:data accounts.user/v1
var Other = declaration.Member[declaration.NoAccess]{Namespace: "accounts"}
`, analyzer.ResourceContract{})
	result, err := analyzer.Analyze(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || !hasCode(result, analyzer.CodeDuplicateSymbol) {
		t.Fatalf("duplicate result = %#v", result)
	}

	reserved := duplicate
	reserved.Packages = []analyzer.Package{{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "generated/models.go", Content: []byte("package model")}}}}
	if _, err := analyzer.Analyze(reserved); !errors.Is(err, analyzer.ErrInvalidSnapshot) {
		t.Fatalf("reserved snapshot error = %v", err)
	}
}

func TestAnalyzeHonorsLimits(t *testing.T) {
	snapshot := sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts"}
`, analyzer.ResourceContract{})
	snapshot.Limits.MaxRoots = 0
	snapshot.Limits.MaxNodes = 1
	result, err := analyzer.Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || !hasCode(result, analyzer.CodeLimitExceeded) {
		t.Fatalf("limit result = %#v", result)
	}
}

func TestAnalyzeMemberIDDoesNotConsumeLocalSymbolLimit(t *testing.T) {
	id := strings.Repeat("a", 124) + ".b/v1"
	snapshot := sourceSnapshot("package model\n\nimport \"github.com/plystra/data/declaration\"\n\n//plystra:data "+id+"\nvar Accounts = declaration.Member[declaration.NoAccess]{Namespace: \"accounts\"}\n", analyzer.ResourceContract{})
	result, err := analyzer.Analyze(snapshot)
	if err != nil || !result.Valid || len(result.Roots) != 1 || result.Roots[0].MemberID != id {
		t.Fatalf("long member ID analysis = %#v, %v", result, err)
	}
	invalid := sourceSnapshot("package model\n\nimport \"github.com/plystra/data/declaration\"\n\n//plystra:data accounts.user/v18446744073709551616\nvar Accounts = declaration.Member[declaration.NoAccess]{Namespace: \"accounts\"}\n", analyzer.ResourceContract{})
	result, err = analyzer.Analyze(invalid)
	if err != nil || result.Valid || !hasCode(result, analyzer.CodeInvalidID) {
		t.Fatalf("overflow member ID analysis = %#v, %v", result, err)
	}
}

func TestAnalyzeTruncatesDiagnosticsByDeclaredBounds(t *testing.T) {
	snapshot := sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data bad/v1
//plystra:data also/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts"}
`, analyzer.ResourceContract{})
	snapshot.Limits.MaxDiagnostics = 1
	snapshot.Limits.MaxDiagnosticBytes = 1024
	result, err := analyzer.Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || !result.Truncated || len(result.Diagnostics) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestAnalyzeDoesNotTreatZeroDiagnosticBudgetAsValid(t *testing.T) {
	snapshot := sourceSnapshot(`package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts", Tables: []declaration.Table{{ID: "users", Indexes: []declaration.Index{{ID: "bad", Columns: []string{"missing"}}}}}}
`, analyzer.ResourceContract{})
	snapshot.Limits.MaxDiagnosticBytes = 1
	result, err := analyzer.Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid || !result.Truncated || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func sourceSnapshot(body string, resources ...analyzer.ResourceContract) analyzer.Snapshot {
	return sourceSnapshotFile("models.go", body, resources...)
}

func sourceSnapshotFile(fileName, body string, resources ...analyzer.ResourceContract) analyzer.Snapshot {
	validResources := make([]analyzer.ResourceContract, 0, len(resources))
	for _, resource := range resources {
		if resource.ImportPath != "" || resource.TypeName != "" || resource.ID != "" {
			validResources = append(validResources, resource)
		}
	}
	return analyzer.Snapshot{
		Packages: []analyzer.Package{
			{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(`package declaration

type NoAccess struct{}
type Ref struct { Member string; Symbol string }
type ScalarType string
const TypeUUID ScalarType = "uuid"
const TypeInt64 ScalarType = "int64"
type Value struct { Kind ScalarType; Set bool; Parameter string; String string; Int64 int64; Uint64 uint64; Float64 float64; Bool bool; Bytes string }
type Member[Access any] struct { Namespace string; Imports []string; Tables []Table; Queries []Query; Plans []Plan; Migrations []Migration; Access Access }
type Table struct { ID string; Columns []Column; Constraints []Constraint; Indexes []Index }
type Column struct { ID string; Type ScalarType; Nullable bool; Default Value }
type ConstraintKind string
type Constraint struct { ID string; Kind ConstraintKind; Columns []string; ReferencedTable Ref; ReferencedCols []string }
type Index struct { ID string; Columns []string; Unique bool }
type OperationKind string
type Parameter struct { ID string; GoField string; Type ScalarType }
type Assignment struct { Column string; Value Value; Increment bool }
type ColumnRef struct { Table Ref; Column string }
type JoinKind string
const JoinLeft JoinKind = "left"
type JoinCondition struct { Left ColumnRef; Right ColumnRef }
type Join struct { Kind JoinKind; Table Ref; On JoinCondition }
type ConflictAction string
type Conflict struct { Constraint string; Action ConflictAction; UpdateColumns []string }
type Query struct { ID string; Method string; Cardinality Cardinality; Operation OperationKind; Table Ref; Joins []Join; Parameters []Parameter; Assignments []Assignment; Conflict Conflict; Projection []Projection; GroupBy []ColumnRef; Having Predicate; Returning []Projection; Results []ResultField; Predicate Predicate; Ordering []Ordering; Limit uint64; HasLimit bool; Offset uint64; HasOffset bool }
type Projection struct { Table Ref; Column string; Alias string; Aggregate string }
type ResultField struct { ID string; GoField string; Source string; Type ScalarType; Nullable bool }
type PredicateKind string
type Predicate struct { Kind PredicateKind; Left ColumnRef; Right ColumnRef; Value Value; Items []Predicate }
type OrderingDirection string
type Ordering struct { Table Ref; Column string; Direction OrderingDirection }
type Plan struct { ID string; Method string; Statements []PlanStatement }
type PlanStatement struct { ID string; Query Ref; Cardinality Cardinality; Affected AffectedRows }
type Cardinality string
type AffectedRows struct { Set bool; Min uint64; Max uint64; Unbounded bool }
type Migration struct { ID string; Version uint64; Predecessors []Ref; Operations []MigrationOperation }
type MigrationOperationKind string
type MigrationOperation struct { Kind MigrationOperationKind; Table Ref; Column Column; Index Index }
`)}}},
			{ImportPath: "github.com/plystra/data/database", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "resource.go", Content: []byte(`package database
import "context"
type Request struct{}
type Row struct{ID int64}
type Resource interface{ Read(context.Context, Request) ([]Row, error) }
`)}}},
			{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: fileName, Content: []byte(body)}}},
			{ImportPath: "context", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "context.go", Content: []byte("package context\ntype Context interface{}\n")}}},
		},
		Resources: validResources,
	}
}

func hasCode(result analyzer.Result, want analyzer.DiagnosticCode) bool {
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == want {
			return true
		}
	}
	return false
}
