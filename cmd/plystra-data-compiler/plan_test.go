package main_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/protocol"
)

func TestCompilerCommandEmitsBoundAtomicPlanProvider(t *testing.T) {
	model, err := (declaration.Member[struct{}]{Namespace: "accounts", Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}}, Queries: []declaration.Query{{
		ID: "create", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "users"}, Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
		Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}},
		Returning:   []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}}, Plans: []declaration.Plan{{ID: "register", Method: "Register", Parameters: []declaration.Parameter{{ID: "account-id", GoField: "Key", Type: declaration.TypeInt64}}, Statements: []declaration.PlanStatement{{
		ID: "account", Query: declaration.Ref{Symbol: "create"}, Cardinality: declaration.CardinalityExactlyOne, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromParameter: "account-id"}},
	}}, Outputs: []declaration.PlanOutput{{Statement: "account", Result: "id", GoField: "Key"}}}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	roots := []analyzer.Root{{MemberID: "accounts.user/v1", Model: model, AccessID: "accounts.access/v1", AccessPackage: "example.com/accounts", AccessType: "Resource", PlanMethods: []analyzer.PlanMethod{{Name: "Register", RequestPackage: "example.com/accounts", RequestType: "Request", ResultPackage: "example.com/accounts", ResultType: "Row"}}}}
	analyzeDigest, err := analyzer.ResultDigest(roots, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "bound-plan-command",
		Compiler: protocol.CompilerIdentity{ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)), ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26"},
		Build:    protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"}, Analyze: protocol.AnalyzeOutput{Roots: roots, Digest: analyzeDigest, Valid: true}, AnalyzeDigest: analyzeDigest, FrozenModelDigest: "sha256:" + strings.Repeat("3", 64),
		Assignments: []protocol.EmitAssignment{{MemberID: roots[0].MemberID, Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "dataaccess/database.primary"}},
	}
	request.InputDigest, err = protocol.EmitInputDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	var input, stdout, stderr bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".")
	command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	command.Stdin, command.Stdout, command.Stderr = &input, &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("compiler command failed: %v\n%s", err, &stderr)
	}
	var response protocol.EmitResponse
	if err := protocol.ReadFrame(&stdout, &response); err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil || response.Status != protocol.StatusSucceeded || stdout.Len() != 0 {
		t.Fatalf("bound plan emit = %#v, %v", response.Diagnostics, err)
	}
	found := false
	for _, artifact := range response.Output.Artifacts {
		if strings.HasSuffix(artifact.Path, "/provider.go") {
			found = bytes.Contains(artifact.Bytes, []byte("func (provider *Provider) Register(")) && bytes.Contains(artifact.Bytes, []byte(`"account-id": db.Int64Argument(request.Key)`))
		}
	}
	if !found {
		t.Fatal("command omitted the bound plan provider")
	}
}
