package analyzer_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
)

const resourceID = "data.database/v1"

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
		Columns: []declaration.Column{{ID: "id", Type: declaration.TypeUUID}},
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
	if result.Roots[0].DeclarationPos.StartLine == 0 || result.Roots[0].DeclarationPos.Path != "models.go" {
		t.Fatalf("span = %#v", result.Roots[0].DeclarationPos)
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
		Predicate: declaration.Predicate{Kind: "equal", Left: "id", Value: declaration.Value{Kind: "int64", Set: true, Parameter: "user-id"}},
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
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "email", Type: "string"}}}},
	Queries: []declaration.Query{{
		ID: "update-email", Operation: "update", Table: declaration.Ref{Symbol: "users"},
		Assignments: []declaration.Assignment{{Column: "email", Value: declaration.Value{Kind: "string", Set: true, String: "updated"}}},
		Predicate: declaration.Predicate{Kind: "equal", Left: "id", Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
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
	}},
}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid || len(result.Roots) != 1 || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %#v", result)
	}
	joins := result.Roots[0].Model.Queries[0].Joins
	if len(joins) != 1 || joins[0].Kind != "left" || joins[0].On.Left.Column != "team-id" || joins[0].On.Right.Table.Symbol != "teams" {
		t.Fatalf("decoded joins = %#v", joins)
	}
}

func TestAnalyzeRejectsInvalidJoinReferences(t *testing.T) {
	source := `package model

import "github.com/plystra/data/declaration"

//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"teams.member/v1"},
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "team-id", Type: "int64"}}}},
	Queries: []declaration.Query{{ID: "read", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Joins: []declaration.Join{{Kind: "inner", Table: declaration.Ref{Member: "teams.member/v1", Symbol: "teams"}, On: declaration.JoinCondition{
			Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "users"}, Column: "team-id"},
			Right: declaration.ColumnRef{Table: declaration.Ref{Member: "teams.member/v1", Symbol: "teams"}, Column: "id"},
		}}},
	}},
}

//plystra:data teams.member/v1
var Teams = declaration.Member[declaration.NoAccess]{Namespace: "teams", Tables: []declaration.Table{{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}}}
`
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
		Predicate: declaration.Predicate{Kind: "equal", Left: "id", Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
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
	Queries: []declaration.Query{{
		ID: "audit-events",
		Operation: "select",
		Table: declaration.Ref{Member: "orders.audit/v1", Symbol: "events"},
		Projection: []declaration.Projection{{Column: "id"}},
	}},
}

//plystra:data orders.audit/v1
var Audit = declaration.Member[declaration.NoAccess]{
	Namespace: "orders",
	Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: "uuid"}}}},
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
	reserved.Packages = []analyzer.Package{{ImportPath: "example.com/model", Files: []analyzer.SourceFile{{Path: "generated/models.go", Content: []byte("package model")}}}}
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
			{ImportPath: "github.com/plystra/data/declaration", Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(`package declaration

type NoAccess struct{}
type Ref struct { Member string; Symbol string }
type ScalarType string
const TypeUUID ScalarType = "uuid"
type Value struct { Kind ScalarType; Set bool; Parameter string; String string; Int64 int64; Uint64 uint64; Float64 float64; Bool bool; Bytes string }
type Member[Access any] struct { Namespace string; Imports []string; Tables []Table; Queries []Query; Plans []Plan; Migrations []Migration; Access Access }
type Table struct { ID string; Columns []Column; Constraints []Constraint; Indexes []Index }
type Column struct { ID string; Type ScalarType; Nullable bool; Default Value }
type ConstraintKind string
type Constraint struct { ID string; Kind ConstraintKind; Columns []string; ReferencedTable Ref; ReferencedCols []string }
type Index struct { ID string; Columns []string; Unique bool }
type OperationKind string
type Parameter struct { ID string; Type ScalarType }
type Assignment struct { Column string; Value Value }
type ColumnRef struct { Table Ref; Column string }
type JoinKind string
const JoinLeft JoinKind = "left"
type JoinCondition struct { Left ColumnRef; Right ColumnRef }
type Join struct { Kind JoinKind; Table Ref; On JoinCondition }
type Query struct { ID string; Operation OperationKind; Table Ref; Joins []Join; Parameters []Parameter; Assignments []Assignment; Projection []Projection; Returning []Projection; Results []ResultField; Predicate Predicate; Ordering []Ordering; Limit uint64; HasLimit bool; Offset uint64; HasOffset bool }
type Projection struct { Column string; Alias string }
type ResultField struct { ID string; Source string; Type ScalarType; Nullable bool }
type PredicateKind string
type Predicate struct { Kind PredicateKind; Left string; Right string; Value Value; Items []Predicate }
type OrderingDirection string
type Ordering struct { Column string; Direction OrderingDirection }
type Plan struct { ID string; Statements []PlanStatement }
type PlanStatement struct { ID string; Query Ref; Cardinality Cardinality; Affected AffectedRows }
type Cardinality string
type AffectedRows struct { Set bool; Min uint64; Max uint64; Unbounded bool }
type Migration struct { ID string; Version uint64; Predecessors []Ref; Operations []MigrationOperation }
type MigrationOperationKind string
type MigrationOperation struct { Kind MigrationOperationKind; Table Ref; Column Column; Index Index }
`)}}},
			{ImportPath: "github.com/plystra/data/database", Files: []analyzer.SourceFile{{Path: "resource.go", Content: []byte(`package database

type Resource interface { Ping() error }
`)}}},
			{ImportPath: "example.com/model", Files: []analyzer.SourceFile{{Path: fileName, Content: []byte(body)}}},
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
