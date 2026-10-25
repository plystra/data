package project

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/protocol"
)

func TestInvokeSelectedEmitterValidatesProcessAndArtifacts(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(fixture, []byte(`package main
import("os";"github.com/plystra/data/protocol")
func main(){
 mode:=os.Getenv("PLYSTRA_DATA_EMIT_TEST_MODE")
 if mode=="nonzero"{os.Exit(7)}
 if mode=="malformed"{os.Stdout.Write([]byte("not a frame"));return}
 var request protocol.EmitRequest
 if protocol.ReadFrame(os.Stdin,&request)!=nil{os.Exit(8)}
 response:=protocol.HandleEmit(request)
 if mode=="identity"{response.RequestID="another"}
 if mode=="tamper"{response.Output.Artifacts[0].Bytes=[]byte("changed")}
 if mode=="stderr"{os.Stderr.Write([]byte("unexpected diagnostic"))}
 if protocol.WriteFrame(os.Stdout,response)!=nil{os.Exit(9)}
 if mode=="trailing"{os.Stdout.Write([]byte("extra"))}
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "compiler")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// This helper builds a protocol fixture only; it never invokes Go tests.
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-buildvcs=false", "-o", binary, fixture)
	build.Env = append(os.Environ(), "GOWORK=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build compiler fixture: %v: %s", err, output)
	}
	request := emitterProcessRequest(t)
	for _, test := range []struct {
		mode string
		want error
	}{
		{"valid", nil}, {"malformed", ErrCompilerResponse}, {"trailing", ErrCompilerResponse},
		{"identity", ErrCompilerResponse}, {"tamper", ErrCompilerResponse},
		{"nonzero", ErrCompilerInvocation}, {"stderr", ErrCompilerInvocation},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Setenv("PLYSTRA_DATA_EMIT_TEST_MODE", test.mode)
			response, err := invokeSelectedEmitter(ctx, binary, request)
			if !errors.Is(err, test.want) {
				t.Fatalf("invoke = %v, want %v", err, test.want)
			}
			if test.want == nil && (response.Status != protocol.StatusSucceeded || protocol.ValidateEmitResponse(request, response) != nil) {
				t.Fatalf("invalid successful response: %#v", response)
			}
			if test.want != nil && len(response.Output.Artifacts) != 0 {
				t.Fatal("invalid process published artifacts")
			}
		})
	}
	t.Setenv("PLYSTRA_DATA_EMIT_TEST_MODE", "valid")
	request.Assignments[0].Backend = "unsupported/v1"
	request.InputDigest, _ = protocol.EmitInputDigest(request)
	response, err := invokeSelectedEmitter(ctx, binary, request)
	if err != nil || response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 || len(response.Diagnostics) == 0 {
		t.Fatalf("validated compiler rejection lost: %#v, %v", response, err)
	}
}

func emitterProcessRequest(t *testing.T) protocol.EmitRequest {
	t.Helper()
	model, err := (declaration.Member[declaration.NoAccess]{Namespace: "audit"}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	output := protocol.AnalyzeOutput{Valid: true, Roots: []analyzer.Root{{MemberID: "audit.log/v1", Model: model, MigrationOnly: true, AccessPackage: "github.com/plystra/data/declaration", AccessType: "NoAccess"}}}
	output.Digest, _ = analyzer.ResultDigest(output.Roots, nil, false)
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "emit-process-test",
		Compiler: protocol.CompilerIdentity{
			ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)),
			ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath,
			AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: runtime.Version(),
		},
		Build: protocol.BuildContext{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, Analyze: output, AnalyzeDigest: output.Digest,
		FrozenModelDigest: "sha256:" + strings.Repeat("3", 64),
		Assignments:       []protocol.EmitAssignment{{MemberID: "audit.log/v1", Resource: "database.primary", ResourceContract: databaseResource, Provider: postgresProvider, Backend: postgresBackend, AllowedRoot: "dataaccess/database.primary"}},
	}
	request.InputDigest, err = protocol.EmitInputDigest(request)
	if err != nil || protocol.ValidateEmitRequest(request) != nil {
		t.Fatalf("invalid emit fixture: %v", err)
	}
	return request
}
