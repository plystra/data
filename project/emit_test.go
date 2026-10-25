package project_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/project"
	"github.com/plystra/data/protocol"
)

func TestPrepareEmitRequestFreezesValidatedActiveInputs(t *testing.T) {
	analysis, response, assignments, configured := emitInputs(t)
	before, _ := json.Marshal([]any{analysis, response, assignments, configured})
	request, err := project.PrepareEmitRequest("example.com/app", analysis, response, assignments, configured)
	if err != nil || protocol.ValidateEmitRequest(request) != nil {
		t.Fatalf("prepare = %#v, %v", request, err)
	}
	want := []protocol.EmitAssignment{
		{MemberID: "accounts.user/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "dataaccess/database.primary"},
		{MemberID: "audit.log/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "dataaccess/database.primary"},
	}
	if !reflect.DeepEqual(request.Assignments, want) || request.AnalyzeDigest != response.OutputDigest ||
		request.Compiler != analysis.Compiler || request.Limits != analysis.Limits ||
		!reflect.DeepEqual(request.Build.BuildTags, []string{"alpha", "zeta"}) || len(request.MigrationHistory) != 0 {
		t.Fatalf("incorrect frozen input = %#v", request)
	}
	emitted := protocol.HandleEmit(request)
	if emitted.Status != protocol.StatusSucceeded || protocol.ValidateEmitResponse(request, emitted) != nil {
		t.Fatalf("prepared request is not emit-ready: %#v", emitted)
	}
	provider := false
	for _, artifact := range emitted.Output.Artifacts {
		if strings.Contains(artifact.Path, "dormant") {
			t.Fatalf("unassigned member emitted: %s", artifact.Path)
		}
		provider = provider || strings.HasSuffix(artifact.Path, "providers/accounts.user/v1/provider.go")
	}
	if !provider {
		t.Fatal("accepted typed access provider was not emitted")
	}
	after, _ := json.Marshal([]any{analysis, response, assignments, configured})
	if string(before) != string(after) {
		t.Fatal("request preparation mutated its inputs")
	}
	analysis.RequestID = "another-request"
	response.RequestID = analysis.RequestID
	analysis.Build.BuildTags = []string{"alpha", "zeta"}
	assignments[0], assignments[1] = assignments[1], assignments[0]
	reordered, err := project.PrepareEmitRequest("example.com/app", analysis, response, assignments, configured)
	if err != nil || reordered.InputDigest != request.InputDigest || reordered.FrozenModelDigest != request.FrozenModelDigest {
		t.Fatalf("request ID/list order changed frozen identity: %v", err)
	}
}

func TestPrepareEmitRequestBindsAccessSourceBuildAndResourceFacts(t *testing.T) {
	for _, change := range []string{"access", "source", "build", "configuration", "compiler"} {
		t.Run(change, func(t *testing.T) {
			analysis, response, assignments, configured := emitInputs(t)
			original, err := project.PrepareEmitRequest("example.com/app", analysis, response, assignments, configured)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "access":
				assignments[1].Access = "access.another"
			case "source":
				analysis.Snapshot.Packages[0].Files[0].Digest = "sha256:" + strings.Repeat("3", 64)
			case "build":
				analysis.Build.BuildTags = []string{"other"}
			case "configuration":
				configured.Instances[1].SourceLine++
			case "compiler":
				analysis.Compiler.ModuleVersion = "v0.0.0-other"
			}
			analysis.InputDigest, _ = protocol.AnalyzeInputDigest(analysis.Snapshot, analysis.Build)
			response.InputDigest = analysis.InputDigest
			changed, err := project.PrepareEmitRequest("example.com/app", analysis, response, assignments, configured)
			if err != nil || changed.FrozenModelDigest == original.FrozenModelDigest || changed.InputDigest == original.InputDigest {
				t.Fatalf("%s was not bound: %v", change, err)
			}
		})
	}
}

func TestPrepareEmitRequestRejectsUnvalidatedSelections(t *testing.T) {
	for _, change := range []string{"analyze identity", "analyze digest", "missing root", "missing access", "missing database", "backend", "module"} {
		t.Run(change, func(t *testing.T) {
			analysis, response, assignments, configured := emitInputs(t)
			module := "example.com/app"
			switch change {
			case "analyze identity":
				response.RequestID = "other"
			case "analyze digest":
				response.OutputDigest = "sha256:" + strings.Repeat("3", 64)
			case "missing root":
				assignments[0].MemberID = "missing.member/v1"
			case "missing access":
				assignments[1].Access = ""
			case "missing database":
				configured.Instances = configured.Instances[:1]
			case "backend":
				configured.Instances[1].Provider = "example.com/unsupported.New"
			case "module":
				module = "example.com/other"
			}
			request, err := project.PrepareEmitRequest(module, analysis, response, assignments, configured)
			if err == nil || !reflect.DeepEqual(request, protocol.EmitRequest{}) {
				t.Fatalf("invalid %s accepted: %#v, %v", change, request, err)
			}
		})
	}
}

func TestPrepareEmitRequestPreservesValidatedAnalysisDiagnostics(t *testing.T) {
	analysis, _, assignments, configured := emitInputs(t)
	response := protocol.AnalyzeResponse{
		Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: analysis.RequestID,
		InputDigest: analysis.InputDigest, Status: protocol.StatusRejected,
		Diagnostics: []analyzer.Diagnostic{{Code: analyzer.CodeInvalidSnapshot, Message: "analyze snapshot rejected", Action: "Recreate the private source snapshot."}},
	}
	_, err := project.PrepareEmitRequest("example.com/app", analysis, response, assignments, configured)
	var rejected *project.EmitAnalysisError
	if !errors.As(err, &rejected) || !reflect.DeepEqual(rejected.Diagnostics, response.Diagnostics) {
		t.Fatalf("validated diagnostics lost: %v", err)
	}
}

func emitInputs(t *testing.T) (protocol.AnalyzeRequest, protocol.AnalyzeResponse, []project.Assignment, project.ConfiguredResources) {
	t.Helper()
	snapshot := analyzer.Snapshot{Packages: []analyzer.Package{
		{ImportPath: "context", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "context.go", Content: []byte("package context\ntype Context interface{}\n")}}},
		{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(`package declaration
type Ref struct{Symbol string}
type ScalarType string
type Cardinality string
type OperationKind string
type NoAccess struct{}
type Member[A any] struct{Namespace string;Tables []Table;Queries []Query}
type Table struct{ID string;Columns []Column}
type Column struct{ID string;Type ScalarType}
type Query struct{ID string;Method string;Cardinality Cardinality;Operation OperationKind;Table Ref;Projection []Projection;Results []ResultField}
type Projection struct{Column string}
type ResultField struct{ID string;GoField string;Source string;Type ScalarType}
`)}}},
		{ImportPath: "example.com/app/access", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "access.go", Content: []byte(`package access
import "context"
//plystra:resource accounts.access/v1
type Resource interface{Read(context.Context, Request)([]Row,error)}
type Request struct{}
type Row struct{ID int64}
`)}}},
		{ImportPath: "example.com/app/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte(`package model
import("example.com/app/access";"github.com/plystra/data/declaration")
//plystra:data accounts.user/v1
var Accounts=declaration.Member[access.Resource]{Namespace:"accounts",Tables:[]declaration.Table{{ID:"users",Columns:[]declaration.Column{{ID:"id",Type:"int64"}}}},Queries:[]declaration.Query{{ID:"read",Method:"Read",Cardinality:"many",Operation:"select",Table:declaration.Ref{Symbol:"users"},Projection:[]declaration.Projection{{Column:"id"}},Results:[]declaration.ResultField{{ID:"id",GoField:"ID",Source:"id",Type:"int64"}}}}}
//plystra:data audit.log/v1
var Audit=declaration.Member[declaration.NoAccess]{Namespace:"audit"}
//plystra:data dormant.member/v1
var Dormant=declaration.Member[declaration.NoAccess]{Namespace:"dormant"}
`)}}},
	}, Resources: []analyzer.ResourceContract{{ImportPath: "example.com/app/access", TypeName: "Resource", ID: "accounts.access/v1"}}}
	manifest, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	analysis := protocol.AnalyzeRequest{
		Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: "plan-test",
		Compiler: protocol.CompilerIdentity{
			ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)),
			ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath,
			AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: runtime.Version(),
		},
		Build:    protocol.BuildContext{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, BuildTags: []string{"zeta", "alpha"}},
		Snapshot: manifest, Limits: manifest.Limits,
	}
	analysis.InputDigest, err = protocol.AnalyzeInputDigest(manifest, analysis.Build)
	if err != nil {
		t.Fatal(err)
	}
	response := protocol.HandleAnalyze(analysis)
	if response.Status != protocol.StatusSucceeded || protocol.ValidateAnalyzeResponse(analysis, response) != nil {
		t.Fatalf("analysis fixture rejected: %#v", response)
	}
	assignments := []project.Assignment{{MemberID: "audit.log/v1", Resource: "database.primary"}, {MemberID: "accounts.user/v1", Resource: "database.primary", Access: "access.generated"}}
	configured := project.ConfiguredResources{ModulePath: "example.com/app", Instances: []project.ConfiguredResource{
		{Name: "access.generated", Provider: "example.com/app/dataaccess.New", SourcePath: "plystra.yaml", SourceLine: 3, SourceColumn: 7},
		{Name: "database.primary", Provider: "github.com/plystra/data/postgres.New", SourcePath: "plystra.yaml", SourceLine: 5, SourceColumn: 7, ProviderVisible: true, ResourceID: "data.database/v1"},
	}}
	return analysis, response, assignments, configured
}
