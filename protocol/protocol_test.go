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
	if output.Len() != 0 {
		t.Fatal("protocol emitted more than one frame")
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
	request.Snapshot.Packages = append(request.Snapshot.Packages, analyzer.Package{
		ImportPath: "example.com/support", RootEligibility: analyzer.RootSupport,
		Files: []analyzer.SourceFile{{Path: "support.go", Content: []byte(`package support
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Injected = declaration.Member[declaration.NoAccess]{Namespace: "injected"}
`)}},
	})
	var err error
	request.InputDigest, err = protocol.AnalyzeInputDigest(request.Snapshot, request.Build)
	if err != nil {
		t.Fatal(err)
	}
	response := protocol.HandleAnalyze(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Roots) != 1 || response.Output.Roots[0].PackagePath != "example.com/model" {
		t.Fatalf("support package injected a root: %#v", response)
	}
	request.Snapshot.Packages[2].RootEligibility = analyzer.RootEligible
	if response = protocol.HandleAnalyze(request); response.Status != protocol.StatusRejected || !hasDiagnosticCode(response.Diagnostics, analyzer.CodeInvalidSnapshot) {
		t.Fatalf("changed eligibility retained input digest: %#v", response)
	}
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
	request.Snapshot.Packages[1].Files[0].Content = []byte(`package model
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{Namespace: "accounts", Imports: []string{"orders.audit/v1"}}
`)
	digest, err := protocol.AnalyzeInputDigest(request.Snapshot, request.Build)
	if err != nil {
		t.Fatal(err)
	}
	request.InputDigest = digest
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
		MaxFrameBytes: protocol.MaxFrameBytes, MaxArtifacts: protocol.MaxArtifacts,
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
	valid := `{"schema":"plystra.data-compiler-distribution/v1","module_path":"github.com/plystra/data","command_import_path":"github.com/plystra/data/cmd/plystra-data-compiler","analyze_protocol":"plystra.data-analyze/v1","emit_protocol":"plystra.data-emit/v1","declaration_language":"plystra.data-declaration/v1","limits":{"max_roots":1024,"max_nodes":65536,"max_imports":1024,"max_nesting":64,"max_symbol_bytes":128,"max_diagnostics":256,"max_diagnostic_bytes":262144,"max_frame_bytes":16777216,"max_artifacts":4096,"max_artifact_bytes":4194304,"max_artifact_path_bytes":512}}`
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
	snapshot := analyzer.Snapshot{
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
	build := protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"}
	digest, err := protocol.AnalyzeInputDigest(snapshot, build)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.AnalyzeRequest{
		Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: "request-1",
		Compiler: protocol.CompilerIdentity{
			ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: testModuleChecksum(), ManifestDigest: "sha256:" + strings.Repeat("2", 64),
			CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26",
		},
		Build: build, Limits: snapshot.Limits, InputDigest: digest, Snapshot: snapshot,
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
