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

func TestCompilerCommandEmitsDirectMutationProviders(t *testing.T) {
	const contract = "example.com/accounts"
	var queries []declaration.Query
	var methods []analyzer.QueryMethod
	for _, operation := range []struct {
		kind        declaration.OperationKind
		name        string
		cardinality declaration.Cardinality
	}{{declaration.OperationInsert, "Insert", declaration.CardinalityExactlyOne}, {declaration.OperationUpdate, "Update", declaration.CardinalityZeroOrOne}, {declaration.OperationDelete, "Delete", declaration.CardinalityMany}} {
		query := declaration.Query{ID: string(operation.kind), Method: operation.name, Operation: operation.kind, Table: declaration.Ref{Symbol: "users"}, Parameters: []declaration.Parameter{{ID: "id", GoField: "Key", Type: declaration.TypeInt64}}, Cardinality: operation.cardinality, Returning: []declaration.Projection{{Column: "id", Alias: "user-id"}}, Results: []declaration.ResultField{{ID: "id", GoField: "Key", Source: "user-id", Type: declaration.TypeInt64}}}
		if operation.kind != declaration.OperationInsert {
			query.Predicate = declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}
		}
		if operation.kind != declaration.OperationDelete {
			query.Assignments = []declaration.Assignment{{Column: "id", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}}
		}
		queries = append(queries, query)
		methods = append(methods, analyzer.QueryMethod{Name: query.Method, Cardinality: query.Cardinality, RequestPackage: contract, RequestType: "Request", RowPackage: contract, RowType: "Row"})
		query.ID += "-count"
		query.Method += "Count"
		query.Cardinality, query.Returning, query.Results = declaration.CardinalityNone, nil, nil
		queries = append(queries, query)
		methods = append(methods, analyzer.QueryMethod{Name: query.Method, Cardinality: query.Cardinality, RequestPackage: contract, RequestType: "Request"})
	}
	model, err := (declaration.Member[struct{}]{Namespace: "accounts", Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}}, Queries: queries}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	roots := []analyzer.Root{{MemberID: "accounts.user/v1", Model: model, AccessID: "accounts.access/v1", AccessPackage: contract, AccessType: "Resource", QueryMethods: methods}}
	analyzeDigest, err := analyzer.ResultDigest(roots, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "mutation-command",
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
		t.Fatalf("mutation emit = %#v, %v", response.Diagnostics, err)
	}
	var source string
	for _, artifact := range response.Output.Artifacts {
		if strings.HasSuffix(artifact.Path, "/provider.go") {
			if source != "" {
				t.Fatal("duplicate provider artifact")
			}
			source = string(artifact.Bytes)
		}
	}
	for _, method := range methods {
		if !strings.Contains(source, "func (provider *Provider) "+method.Name+"(") {
			t.Fatalf("command omitted mutation method %s", method.Name)
		}
	}
	for _, want := range []string{"(contract0.Row, error)", "(*contract0.Row, error)", "([]contract0.Row, error)", "(uint64, error)", "db.AtLeastAffectedRows(0)", "db.Int64Argument(request.Key)"} {
		if !strings.Contains(source, want) {
			t.Fatalf("emitted provider omitted %q", want)
		}
	}
}
