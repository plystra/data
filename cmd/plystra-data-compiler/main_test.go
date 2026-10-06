package main

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

func TestCompilerCommandEmitsOnlyOneStagedFrame(t *testing.T) {
	model, err := (declaration.Member[struct{}]{
		Namespace: "accounts",
		Tables:    []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}},
		Queries: []declaration.Query{{
			ID: "delete-account", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "accounts"},
			Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
			Predicate:  declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
			Returning:  []declaration.Projection{{Column: "id", Alias: "account-id"}},
			Results:    []declaration.ResultField{{ID: "id", Source: "account-id", Type: declaration.TypeInt64}},
		}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	roots := []analyzer.Root{{MemberID: "accounts.user/v1", Model: model, MigrationOnly: true}}
	analyzeDigest, err := analyzer.ResultDigest(roots, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "public-command",
		Compiler: protocol.CompilerIdentity{ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)), ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26"},
		Build:    protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"}, Analyze: protocol.AnalyzeOutput{Roots: roots, Digest: analyzeDigest, Valid: true}, AnalyzeDigest: analyzeDigest,
		FrozenModelDigest: "sha256:" + strings.Repeat("3", 64),
		Assignments:       []protocol.EmitAssignment{{MemberID: "accounts.user/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "generated/data/database.primary"}},
	}
	request.InputDigest, err = protocol.EmitInputDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".")
	command.Env = append(os.Environ(), "GOWORK=off")
	command.Stdin = &input
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("compiler command failed: %v; stderr=%s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("compiler wrote stderr: %q", stderr.String())
	}
	var response protocol.EmitResponse
	if err := protocol.ReadFrame(&stdout, &response); err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil || stdout.Len() != 0 || len(response.Output.Artifacts) != 3 {
		t.Fatalf("invalid command response: %v, bytes=%d, artifacts=%d", err, stdout.Len(), len(response.Output.Artifacts))
	}
	for _, artifact := range response.Output.Artifacts {
		if strings.HasSuffix(artifact.Path, "/delete-account.sql") && !bytes.Contains(artifact.Bytes, []byte(`RETURNING "id" AS "account-id";`)) {
			t.Fatalf("query artifact does not contain typed RETURNING: %q", artifact.Bytes)
		}
	}
	after, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("compiler wrote files in the project directory")
	}
}

func TestCompilerCommandDoesNotDependOnCorePackages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "list", "-deps", ".")
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list compiler dependencies: %v", err)
	}
	for _, path := range strings.Fields(string(output)) {
		if path == "github.com/plystra/kernel" || strings.HasPrefix(path, "github.com/plystra/kernel/") ||
			path == "github.com/plystra/cli" || strings.HasPrefix(path, "github.com/plystra/cli/") {
			t.Fatalf("compiler depends on Core package %q", path)
		}
	}
}
