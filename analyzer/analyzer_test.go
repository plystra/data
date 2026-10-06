package analyzer_test

import (
	"errors"
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
			code: `var Accounts = declaration.Member[declaration.NoAccess]{"accounts", nil}`,
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
	if !result.Truncated || len(result.Diagnostics) != 1 {
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
type ScalarType string
const TypeUUID ScalarType = "uuid"
type Column struct { ID string; Type ScalarType }
type Table struct { ID string; Columns []Column }
type Member[Access any] struct { Namespace string; Tables []Table }
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
