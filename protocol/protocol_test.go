package protocol_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/protocol"
)

func TestAnalyzeFrameRoundTripAndNoGeneratedOutput(t *testing.T) {
	request := validRequest(t)
	var input bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := protocol.Run(&input, &output); err != nil {
		t.Fatal(err)
	}
	var response protocol.AnalyzeResponse
	if err := protocol.ReadFrame(&output, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != protocol.StatusSucceeded || response.RequestID != request.RequestID || response.InputDigest != request.InputDigest {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Output.Roots) != 1 || response.OutputDigest == "" || len(response.Diagnostics) != 0 {
		t.Fatalf("output = %#v", response.Output)
	}
	if err := protocol.ValidateAnalyzeResponse(request, response); err != nil {
		t.Fatalf("response validation = %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("protocol emitted more than one frame")
	}
}

func TestValidateAnalyzeResponseRejectsTampering(t *testing.T) {
	request := validRequest(t)
	base := protocol.HandleAnalyze(request)
	if err := protocol.ValidateAnalyzeResponse(request, base); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*protocol.AnalyzeResponse)
	}{
		{"request identity", func(value *protocol.AnalyzeResponse) { value.RequestID = "other" }},
		{"input digest", func(value *protocol.AnalyzeResponse) { value.InputDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"output digest", func(value *protocol.AnalyzeResponse) { value.OutputDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"status", func(value *protocol.AnalyzeResponse) { value.Status = protocol.StatusRejected }},
		{"truncation", func(value *protocol.AnalyzeResponse) { value.Truncated = true }},
		{"source digest", func(value *protocol.AnalyzeResponse) {
			value.Output.Roots[0].SourceDigest = "sha256:" + strings.Repeat("0", 64)
		}},
		{"model", func(value *protocol.AnalyzeResponse) { value.Output.Roots[0].Model.Namespace = "changed" }},
		{"access", func(value *protocol.AnalyzeResponse) { value.Output.Roots[0].AccessID = "invented.access/v1" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			changed.Output.Roots = append([]analyzer.Root(nil), base.Output.Roots...)
			test.edit(&changed)
			if err := protocol.ValidateAnalyzeResponse(request, changed); err == nil {
				t.Fatalf("tampered response accepted: %#v", changed)
			}
		})
	}
	changed := base
	changed.Output.Roots = append([]analyzer.Root(nil), base.Output.Roots...)
	changed.Output.Roots[0].AccessID = "invented.access/v1"
	digest, err := analyzer.ResultDigest(changed.Output.Roots, changed.Diagnostics, changed.Truncated)
	if err != nil {
		t.Fatal(err)
	}
	changed.OutputDigest = digest
	changed.Output.Digest = changed.OutputDigest
	if err := protocol.ValidateAnalyzeResponse(request, changed); err == nil {
		t.Fatal("rehashed access identity was accepted")
	}
	changed = base
	changed.Diagnostics = []analyzer.Diagnostic{{Code: analyzer.CodeTypeCheck, Message: "bad", Action: "fix", PackagePath: "example.com/model", FilePath: "C:/private/source.go", Span: analyzer.Span{Path: "C:/private/source.go"}}}
	digest, err = analyzer.ResultDigest(changed.Output.Roots, changed.Diagnostics, changed.Truncated)
	if err != nil {
		t.Fatal(err)
	}
	changed.OutputDigest = digest
	changed.Output.Digest = changed.OutputDigest
	changed.Output.Valid = false
	changed.Status = protocol.StatusRejected
	if err := protocol.ValidateAnalyzeResponse(request, changed); err == nil {
		t.Fatal("rehashed host-path diagnostic was accepted")
	}
	request.InputDigest = "sha256:" + strings.Repeat("0", 64)
	if err := protocol.ValidateAnalyzeResponse(request, protocol.HandleAnalyze(request)); err == nil {
		t.Fatal("invalid request was accepted with a rejected response")
	}
}

func TestValidateAnalyzeResponseBindsTypedQueryMethods(t *testing.T) {
	request := validRequest(t)
	replaceRequestSnapshot(t, &request, typedAccessSnapshot())
	base := protocol.HandleAnalyze(request)
	if base.Status != protocol.StatusSucceeded || len(base.Output.Roots) != 1 || len(base.Output.Roots[0].QueryMethods) != 1 {
		t.Fatalf("typed access response = %#v", base)
	}
	if err := protocol.ValidateAnalyzeResponse(request, base); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*analyzer.Root)
	}{
		{name: "missing", edit: func(root *analyzer.Root) { root.QueryMethods = nil }},
		{name: "wrong cardinality", edit: func(root *analyzer.Root) { root.QueryMethods[0].Cardinality = "exactly_one" }},
		{name: "unknown request package", edit: func(root *analyzer.Root) { root.QueryMethods[0].RequestPackage = "example.com/other" }},
		{name: "unexported row", edit: func(root *analyzer.Root) { root.QueryMethods[0].RowType = "row" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			changed.Output.Roots = append([]analyzer.Root(nil), base.Output.Roots...)
			changed.Output.Roots[0].QueryMethods = append([]analyzer.QueryMethod(nil), base.Output.Roots[0].QueryMethods...)
			test.edit(&changed.Output.Roots[0])
			var err error
			changed.OutputDigest, err = analyzer.ResultDigest(changed.Output.Roots, changed.Diagnostics, changed.Truncated)
			if err != nil {
				t.Fatal(err)
			}
			changed.Output.Digest = changed.OutputDigest
			if err := protocol.ValidateAnalyzeResponse(request, changed); err == nil {
				t.Fatal("rehashed invalid query method inventory was accepted")
			}
		})
	}
}

func TestValidateAnalyzeResponseBindsCallablePlanMethod(t *testing.T) {
	request := validRequest(t)
	replaceRequestSnapshot(t, &request, callablePlanSnapshot())
	base := protocol.HandleAnalyze(request)
	if base.Status != protocol.StatusSucceeded || len(base.Output.Roots) != 1 || len(base.Output.Roots[0].PlanMethods) != 1 {
		t.Fatalf("callable plan response = %#v", base)
	}
	if err := protocol.ValidateAnalyzeResponse(request, base); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*analyzer.Root)
	}{
		{name: "missing", edit: func(root *analyzer.Root) { root.PlanMethods = nil }},
		{name: "unknown request package", edit: func(root *analyzer.Root) { root.PlanMethods[0].RequestPackage = "example.com/other" }},
		{name: "unexported result", edit: func(root *analyzer.Root) { root.PlanMethods[0].ResultType = "result" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			changed.Output.Roots = append([]analyzer.Root(nil), base.Output.Roots...)
			changed.Output.Roots[0].PlanMethods = append([]analyzer.PlanMethod(nil), base.Output.Roots[0].PlanMethods...)
			test.edit(&changed.Output.Roots[0])
			var err error
			changed.OutputDigest, err = analyzer.ResultDigest(changed.Output.Roots, changed.Diagnostics, changed.Truncated)
			if err != nil {
				t.Fatal(err)
			}
			changed.Output.Digest = changed.OutputDigest
			if err := protocol.ValidateAnalyzeResponse(request, changed); err == nil {
				t.Fatal("rehashed invalid plan method inventory was accepted")
			}
		})
	}
}

func TestValidateAnalyzeResponseBindsReturningMutationMethod(t *testing.T) {
	request := validRequest(t)
	replaceRequestSnapshot(t, &request, returningMutationSnapshot())
	response := protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Roots) != 1 || len(response.Output.Roots[0].QueryMethods) != 1 ||
		response.Output.Roots[0].QueryMethods[0].Cardinality != "exactly_one" {
		t.Fatalf("returning mutation response = %#v", response)
	}
	if err := protocol.ValidateAnalyzeResponse(request, response); err != nil {
		t.Fatal(err)
	}
	response.Output.Roots[0].QueryMethods[0].Cardinality = "many"
	var err error
	response.OutputDigest, err = analyzer.ResultDigest(response.Output.Roots, response.Diagnostics, response.Truncated)
	if err != nil {
		t.Fatal(err)
	}
	response.Output.Digest = response.OutputDigest
	if err := protocol.ValidateAnalyzeResponse(request, response); err == nil {
		t.Fatal("rehashed mismatched mutation cardinality was accepted")
	}
}

func returningMutationSnapshot() analyzer.Snapshot {
	snapshot := typedAccessSnapshot()
	snapshot.Packages[1].Files[0].Content = []byte(`package declaration
type Ref struct{Symbol string}
type ScalarType string
type Cardinality string
type OperationKind string
type Member[A any] struct{Namespace string;Tables []Table;Queries []Query;Access A}
type Table struct{ID string;Columns []Column}
type Column struct{ID string;Type ScalarType}
type Query struct{ID string;Method string;Cardinality Cardinality;Operation OperationKind;Table Ref;Assignments []Assignment;Returning []Projection;Results []ResultField}
type Assignment struct{Column string;Value Value}
type Value struct{Kind ScalarType;Set bool;Int64 int64}
type Projection struct{Column string}
type ResultField struct{ID string;GoField string;Source string;Type ScalarType}
`)
	snapshot.Packages[2].Files[0].Content = []byte(`package access
import "context"
//plystra:resource accounts.access/v1
type Resource interface{Create(context.Context, Request)(Row,error)}
type Request struct{}
type Row struct{ID int64}
`)
	snapshot.Packages[3].Files[0].Content = []byte(`package model
import("example.com/access";"github.com/plystra/data/declaration")
//plystra:data accounts.user/v1
var Accounts=declaration.Member[access.Resource]{Namespace:"accounts",Tables:[]declaration.Table{{ID:"users",Columns:[]declaration.Column{{ID:"id",Type:"int64"}}}},Queries:[]declaration.Query{{ID:"create",Method:"Create",Cardinality:"exactly_one",Operation:"insert",Table:declaration.Ref{Symbol:"users"},Assignments:[]declaration.Assignment{{Column:"id",Value:declaration.Value{Kind:"int64",Set:true,Int64:1}}},Returning:[]declaration.Projection{{Column:"id"}},Results:[]declaration.ResultField{{ID:"id",GoField:"ID",Source:"id",Type:"int64"}}}}}
`)
	return snapshot
}

func TestValidateAnalyzeResponseBindsAffectedRowMutationMethod(t *testing.T) {
	request := validRequest(t)
	snapshot := returningMutationSnapshot()
	snapshot.Packages[2].Files[0].Content = []byte(strings.Replace(string(snapshot.Packages[2].Files[0].Content), "(Row,error)", "(uint64,error)", 1))
	model := string(snapshot.Packages[3].Files[0].Content)
	model = strings.Replace(model, `Cardinality:"exactly_one"`, `Cardinality:"none"`, 1)
	model = strings.Replace(model, `,Returning:[]declaration.Projection{{Column:"id"}},Results:[]declaration.ResultField{{ID:"id",GoField:"ID",Source:"id",Type:"int64"}}`, "", 1)
	snapshot.Packages[3].Files[0].Content = []byte(model)
	replaceRequestSnapshot(t, &request, snapshot)
	response := protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Roots) != 1 || len(response.Output.Roots[0].QueryMethods) != 1 ||
		response.Output.Roots[0].QueryMethods[0].Cardinality != "none" ||
		response.Output.Roots[0].QueryMethods[0].RowPackage != "" || response.Output.Roots[0].QueryMethods[0].RowType != "" {
		t.Fatalf("affected-row mutation response = %#v", response)
	}
	if err := protocol.ValidateAnalyzeResponse(request, response); err != nil {
		t.Fatal(err)
	}
	response.Output.Roots[0].QueryMethods[0].RowPackage = "example.com/access"
	response.Output.Roots[0].QueryMethods[0].RowType = "Row"
	var err error
	response.OutputDigest, err = analyzer.ResultDigest(response.Output.Roots, response.Diagnostics, response.Truncated)
	if err != nil {
		t.Fatal(err)
	}
	response.Output.Digest = response.OutputDigest
	if err := protocol.ValidateAnalyzeResponse(request, response); err == nil {
		t.Fatal("rehashed unexpected affected-row row type was accepted")
	}
}

func TestValidateAnalyzeRequestRejectsDigestMismatch(t *testing.T) {
	request := validRequest(t)
	request.InputDigest = "sha256:" + strings.Repeat("0", 64)
	response := protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusRejected || len(response.Diagnostics) != 1 {
		t.Fatalf("response = %#v", response)
	}
}

func TestAnalyzeInputDigestBindsResourceContracts(t *testing.T) {
	request := validRequest(t)
	request.Snapshot.Resources = []analyzer.ResourceContract{{ImportPath: "example.com/access", TypeName: "Resource", ID: "accounts.access/v1"}}
	if err := protocol.ValidateAnalyzeRequest(request); err == nil {
		t.Fatal("request accepted a Resource identity outside the input digest")
	}
	updated, err := protocol.AnalyzeInputDigest(request.Snapshot, request.Build)
	if err != nil || updated == request.InputDigest {
		t.Fatalf("Resource identity did not change input digest: %s, %v", updated, err)
	}
}

func TestValidateAnalyzeRequestRequiresGoModuleChecksum(t *testing.T) {
	request := validRequest(t)
	request.Compiler.ModuleChecksum = "sha256:" + strings.Repeat("1", 64)
	response := protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusRejected || len(response.Diagnostics) != 1 {
		t.Fatalf("response = %#v", response)
	}
}

func TestAnalyzeInputDigestBindsBuildContext(t *testing.T) {
	request := validRequest(t)
	first, err := protocol.AnalyzeInputDigest(request.Snapshot, request.Build)
	if err != nil {
		t.Fatal(err)
	}
	request.Build.GOOS = "linux"
	second, err := protocol.AnalyzeInputDigest(request.Snapshot, request.Build)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("build context did not change the analyze input digest")
	}
	request.InputDigest = first
	response := protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusRejected {
		t.Fatalf("response = %#v", response)
	}
}

func TestAnalyzeProtocolEnforcesPackageRootEligibility(t *testing.T) {
	request := validRequest(t)
	snapshot := validSourceSnapshot()
	snapshot.Packages = append(snapshot.Packages, analyzer.Package{
		ImportPath: "example.com/support", RootEligibility: analyzer.RootSupport,
		Files: []analyzer.SourceFile{{Path: "support.go", Content: []byte(`package support
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Injected = declaration.Member[declaration.NoAccess]{Namespace: "injected"}
`)}},
	})
	replaceRequestSnapshot(t, &request, snapshot)
	response := protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Roots) != 1 || response.Output.Roots[0].PackagePath != "example.com/model" {
		t.Fatalf("support package injected a root: %#v", response)
	}
	for index := range request.Snapshot.Packages {
		if request.Snapshot.Packages[index].ImportPath == "example.com/support" {
			request.Snapshot.Packages[index].RootEligibility = analyzer.RootEligible
		}
	}
	if response = protocol.HandleAnalyze(request); response.Status != protocol.StatusRejected || !hasDiagnosticCode(response.Diagnostics, analyzer.CodeInvalidSnapshot) {
		t.Fatalf("changed eligibility retained input digest: %#v", response)
	}
	var err error
	request.InputDigest, err = protocol.AnalyzeInputDigest(request.Snapshot, request.Build)
	if err != nil {
		t.Fatal(err)
	}
	response = protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusRejected || !hasDiagnosticCode(response.Diagnostics, analyzer.CodeDuplicateSymbol) {
		t.Fatalf("eligible duplicate root = %#v", response)
	}
}

func TestAnalyzeProtocolRejectsInvalidRootEligibility(t *testing.T) {
	for _, eligibility := range []analyzer.RootEligibility{"", "unknown"} {
		request := validRequest(t)
		request.Snapshot.Packages[1].RootEligibility = eligibility
		response := protocol.HandleAnalyze(request)
		if response.Status != protocol.StatusRejected || !hasDiagnosticCode(response.Diagnostics, analyzer.CodeInvalidSnapshot) {
			t.Fatalf("eligibility %q response = %#v", eligibility, response)
		}
	}
}

func TestAnalyzeFramePreservesMemberReferenceDiagnostics(t *testing.T) {
	request := validRequest(t)
	snapshot := validSourceSnapshot()
	snapshot.Packages[1].Files[0].Content = []byte(`package model
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts", Imports: []string{"orders.audit/v1"}}
`)
	replaceRequestSnapshot(t, &request, snapshot)
	response := protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusRejected || response.Output.Valid || !hasDiagnosticCode(response.Diagnostics, analyzer.CodeUnresolvedReference) {
		t.Fatalf("response = %#v", response)
	}
}

func TestReadFrameRejectsOversizedPayload(t *testing.T) {
	var input bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], protocol.MaxFrameBytes+1)
	input.Write(header[:])
	var value struct{}
	if err := protocol.ReadFrame(&input, &value); err == nil {
		t.Fatal("ReadFrame accepted an oversized payload")
	}
}

func TestDistributionManifestMatchesProtocolIdentity(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "..", "plystra-data-compiler.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, sourceDigest, err := protocol.ParseDistributionManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateDistributionManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Limits != (protocol.DistributionLimits{
		MaxRoots: protocol.MaxRoots, MaxNodes: protocol.MaxNodes, MaxImports: protocol.MaxImports,
		MaxNesting: protocol.MaxNesting, MaxSymbolBytes: protocol.MaxSymbolBytes,
		MaxDiagnostics: protocol.MaxDiagnostics, MaxDiagnosticBytes: protocol.MaxDiagnosticBytes,
		MaxFrameBytes: protocol.MaxFrameBytes, MaxSnapshotFiles: protocol.MaxSnapshotFiles,
		MaxSnapshotBytes: protocol.MaxSnapshotBytes, MaxSourceFileBytes: protocol.MaxSourceFileBytes,
		MaxSourcePathBytes: protocol.MaxSourcePathBytes,
		MaxStandardExports: protocol.MaxStandardExports, MaxStandardBytes: protocol.MaxStandardBytes,
		MaxStandardFileBytes: protocol.MaxStandardFileBytes, MaxArtifacts: protocol.MaxArtifacts,
		MaxArtifactBytes: protocol.MaxArtifactBytes, MaxArtifactPathBytes: protocol.MaxArtifactPathBytes,
	}) {
		t.Fatalf("manifest = %#v", manifest)
	}
	first, err := protocol.DistributionManifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manifest.Digest()
	if err != nil || first != second {
		t.Fatalf("manifest digest = %q, %v", second, err)
	}
	if len(first) != len("sha256:")+64 || strings.Contains(first, string(manifestBytes)) {
		t.Fatalf("manifest digest exposes source content: %q", first)
	}
	if len(sourceDigest) != len("sha256:")+64 || sourceDigest == first {
		t.Fatalf("source digest = %q, canonical digest = %q", sourceDigest, first)
	}
	compact := bytes.ReplaceAll(manifestBytes, []byte("\n"), nil)
	_, compactDigest, err := protocol.ParseDistributionManifest(compact)
	if err != nil {
		t.Fatal(err)
	}
	if compactDigest == sourceDigest {
		t.Fatal("source digest ignored published manifest bytes")
	}
}

func TestParseDistributionManifestRejectsInvalidContract(t *testing.T) {
	valid := `{"schema":"plystra.data-compiler-distribution/v1","module_path":"github.com/plystra/data","command_import_path":"github.com/plystra/data/cmd/plystra-data-compiler","analyze_protocol":"plystra.data-analyze/v1","emit_protocol":"plystra.data-emit/v1","declaration_language":"plystra.data-declaration/v1","limits":{"max_roots":1024,"max_nodes":65536,"max_imports":1024,"max_nesting":64,"max_symbol_bytes":128,"max_diagnostics":256,"max_diagnostic_bytes":262144,"max_frame_bytes":16777216,"max_snapshot_files":4096,"max_snapshot_bytes":67108864,"max_source_file_bytes":4194304,"max_source_path_bytes":512,"max_standard_exports":1024,"max_standard_bytes":134217728,"max_standard_file_bytes":16777216,"max_artifacts":4096,"max_artifact_bytes":4194304,"max_artifact_path_bytes":512}}`
	tests := []struct {
		name string
		data string
	}{
		{name: "unknown identity", data: strings.Replace(valid, `"schema":`, `"unknown":`, 1)},
		{name: "unknown limit", data: strings.Replace(valid, `"max_roots":`, `"unknown":`, 1)},
		{name: "missing identity", data: strings.Replace(valid, `"schema":"plystra.data-compiler-distribution/v1",`, "", 1)},
		{name: "wrong identity", data: strings.Replace(valid, "plystra.data-emit/v1", "plystra.data-emit/v2", 1)},
		{name: "zero bound", data: strings.Replace(valid, `"max_roots":1024`, `"max_roots":0`, 1)},
		{name: "out of range bound", data: strings.Replace(valid, `"max_nodes":65536`, `"max_nodes":65537`, 1)},
		{name: "duplicate identity", data: strings.Replace(valid, `"schema":"plystra.data-compiler-distribution/v1",`, `"schema":"plystra.data-compiler-distribution/v1","schema":"plystra.data-compiler-distribution/v1",`, 1)},
		{name: "duplicate limit", data: strings.Replace(valid, `"max_roots":1024,`, `"max_roots":1024,"max_roots":1024,`, 1)},
		{name: "trailing json", data: valid + ` {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := protocol.ParseDistributionManifest([]byte(test.data))
			if !errors.Is(err, protocol.ErrInvalidDistributionManifest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func validRequest(t *testing.T) protocol.AnalyzeRequest {
	snapshot := validSourceSnapshot()
	manifest, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	build := protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"}
	digest, err := protocol.AnalyzeInputDigest(manifest, build)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.AnalyzeRequest{
		Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: "request-1",
		Compiler: protocol.CompilerIdentity{
			ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: testModuleChecksum(), ManifestDigest: "sha256:" + strings.Repeat("2", 64),
			CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26",
		},
		Build: build, Limits: snapshot.Limits, InputDigest: digest, Snapshot: manifest,
	}
}

func validSourceSnapshot() analyzer.Snapshot {
	return analyzer.Snapshot{
		Packages: []analyzer.Package{
			{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(`package declaration
type NoAccess struct{}
type Member[Access any] struct { Namespace string; Imports []string }
`)}}},
			{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte(`package model
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts"}
`)}}},
		},
	}
}

func typedAccessSnapshot() analyzer.Snapshot {
	return analyzer.Snapshot{
		Packages: []analyzer.Package{
			{ImportPath: "context", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "context.go", Content: []byte("package context\ntype Context interface{}\n")}}},
			{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(`package declaration
type Ref struct{Symbol string}
type ScalarType string
type Cardinality string
type OperationKind string
type Member[A any] struct{Namespace string;Tables []Table;Queries []Query;Access A}
type Table struct{ID string;Columns []Column}
type Column struct{ID string;Type ScalarType}
type Query struct{ID string;Method string;Cardinality Cardinality;Operation OperationKind;Table Ref;Projection []Projection;Results []ResultField}
type Projection struct{Column string}
type ResultField struct{ID string;GoField string;Source string;Type ScalarType}
`)}}},
			{ImportPath: "example.com/access", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "access.go", Content: []byte(`package access
import "context"
//plystra:resource accounts.access/v1
type Resource interface{Read(context.Context, Request)([]Row,error)}
type Request struct{}
type Row struct{ID int64}
`)}}},
			{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte(`package model
import("example.com/access";"github.com/plystra/data/declaration")
//plystra:data accounts.user/v1
var Accounts=declaration.Member[access.Resource]{Namespace:"accounts",Tables:[]declaration.Table{{ID:"users",Columns:[]declaration.Column{{ID:"id",Type:"int64"}}}},Queries:[]declaration.Query{{ID:"read",Method:"Read",Cardinality:"many",Operation:"select",Table:declaration.Ref{Symbol:"users"},Projection:[]declaration.Projection{{Column:"id"}},Results:[]declaration.ResultField{{ID:"id",GoField:"ID",Source:"id",Type:"int64"}}}}}
`)}}},
		},
		Resources: []analyzer.ResourceContract{{ImportPath: "example.com/access", TypeName: "Resource", ID: "accounts.access/v1"}},
	}
}

func callablePlanSnapshot() analyzer.Snapshot {
	snapshot := typedAccessSnapshot()
	stub := string(snapshot.Packages[1].Files[0].Content)
	stub = strings.Replace(stub, "Queries []Query;Access A", "Queries []Query;Plans []Plan;Access A", 1)
	stub += "type Plan struct{ID string;Method string;Statements []PlanStatement;Outputs []PlanOutput}\n"
	stub += "type PlanStatement struct{ID string;Query Ref;Cardinality Cardinality}\n"
	stub += "type PlanOutput struct{Statement string;Result string;GoField string}\n"
	snapshot.Packages[1].Files[0].Content = []byte(stub)
	snapshot.Packages[2].Files[0].Content = []byte(`package access
import "context"
//plystra:resource accounts.access/v1
type Resource interface{Read(context.Context, Request)([]Row,error);ReadPlan(context.Context, PlanRequest)(PlanResult,error)}
type Request struct{}
type Row struct{ID int64}
type PlanRequest struct{}
type PlanResult struct{ID int64}
`)
	snapshot.Packages[3].Files[0].Content = []byte(`package model
import("example.com/access";"github.com/plystra/data/declaration")
//plystra:data accounts.user/v1
var Accounts=declaration.Member[access.Resource]{Namespace:"accounts",Tables:[]declaration.Table{{ID:"users",Columns:[]declaration.Column{{ID:"id",Type:"int64"}}}},Queries:[]declaration.Query{{ID:"read",Method:"Read",Cardinality:"many",Operation:"select",Table:declaration.Ref{Symbol:"users"},Projection:[]declaration.Projection{{Column:"id"}},Results:[]declaration.ResultField{{ID:"id",GoField:"ID",Source:"id",Type:"int64"}}}},Plans:[]declaration.Plan{{ID:"read-plan",Method:"ReadPlan",Statements:[]declaration.PlanStatement{{ID:"read",Query:declaration.Ref{Symbol:"read"},Cardinality:"exactly_one"}},Outputs:[]declaration.PlanOutput{{Statement:"read",Result:"id",GoField:"ID"}}}}}
`)
	return snapshot
}

func replaceRequestSnapshot(t *testing.T, request *protocol.AnalyzeRequest, source analyzer.Snapshot) {
	t.Helper()
	manifest, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), source)
	if err != nil {
		t.Fatal(err)
	}
	request.Snapshot = manifest
	request.InputDigest, err = protocol.AnalyzeInputDigest(manifest, request.Build)
	if err != nil {
		t.Fatal(err)
	}
}

func testModuleChecksum() string {
	return "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32))
}

func hasDiagnosticCode(diagnostics []analyzer.Diagnostic, want analyzer.DiagnosticCode) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == want {
			return true
		}
	}
	return false
}
