package protocol_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
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
	var manifest struct {
		Schema              string `json:"schema"`
		ModulePath          string `json:"module_path"`
		CommandImportPath   string `json:"command_import_path"`
		AnalyzeProtocol     string `json:"analyze_protocol"`
		EmitProtocol        string `json:"emit_protocol"`
		DeclarationLanguage string `json:"declaration_language"`
		Limits              struct {
			MaxFrameBytes        int `json:"max_frame_bytes"`
			MaxArtifacts         int `json:"max_artifacts"`
			MaxArtifactBytes     int `json:"max_artifact_bytes"`
			MaxArtifactPathBytes int `json:"max_artifact_path_bytes"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != protocol.DistributionSchema || manifest.ModulePath != protocol.ModulePath || manifest.CommandImportPath != protocol.CommandImportPath || manifest.AnalyzeProtocol != protocol.AnalyzeSchema || manifest.EmitProtocol != protocol.EmitSchema || manifest.DeclarationLanguage != protocol.DeclarationLanguage || manifest.Limits.MaxFrameBytes != protocol.MaxFrameBytes || manifest.Limits.MaxArtifacts != protocol.MaxArtifacts || manifest.Limits.MaxArtifactBytes != protocol.MaxArtifactBytes || manifest.Limits.MaxArtifactPathBytes != protocol.MaxArtifactPathBytes {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func validRequest(t *testing.T) protocol.AnalyzeRequest {
	snapshot := analyzer.Snapshot{
		Packages: []analyzer.Package{
			{ImportPath: "github.com/plystra/data/declaration", Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(`package declaration
type NoAccess struct{}
type Member[Access any] struct { Namespace string; Imports []string }
`)}}},
			{ImportPath: "example.com/model", Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte(`package model
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
			ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "sha256:" + strings.Repeat("1", 64), ManifestDigest: "sha256:" + strings.Repeat("2", 64),
			CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26",
		},
		Build: build, Limits: snapshot.Limits, InputDigest: digest, Snapshot: snapshot,
	}
}

func hasDiagnosticCode(diagnostics []analyzer.Diagnostic, want analyzer.DiagnosticCode) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == want {
			return true
		}
	}
	return false
}
