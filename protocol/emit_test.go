package protocol_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/postgres"
	"github.com/plystra/data/protocol"
)

func TestEmitFrameProducesFrozenStagedPostgresArtifacts(t *testing.T) {
	request := validEmitRequest(t)
	var input bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := protocol.Run(&input, &output); err != nil {
		t.Fatal(err)
	}
	var response protocol.EmitResponse
	if err := protocol.ReadFrame(&output, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != protocol.StatusSucceeded || response.RequestID != request.RequestID || response.InputDigest != request.InputDigest || response.OutputDigest == "" || output.Len() != 0 {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Output.Artifacts) != 3 {
		t.Fatalf("artifacts = %#v", response.Output.Artifacts)
	}
	for _, artifact := range response.Output.Artifacts {
		if !strings.HasPrefix(artifact.Path, "generated/data/database.primary/") || artifact.Mode != 0644 || artifact.Digest != postgres.DigestSQL(string(artifact.Bytes)) || artifact.Backend != "postgres/v1" || artifact.Resource != "database.primary" || artifact.Compiler != request.Compiler || artifact.FrozenModelDigest != request.FrozenModelDigest {
			t.Fatalf("invalid artifact = %#v", artifact)
		}
	}
	if response.Output.Artifacts[0].Path != "generated/data/database.primary/manifest.json" || !bytes.Contains(response.Output.Artifacts[0].Bytes, []byte("plystra.data-instance-manifest/v1")) {
		t.Fatalf("manifest artifact = %#v", response.Output.Artifacts[0])
	}
	if response.Output.Artifacts[1].Path != "generated/data/database.primary/migrations/accounts.user/v1/create-accounts.sql" || response.Output.Artifacts[1].Migration == nil || response.Output.Artifacts[1].Migration.ArtifactDigest != response.Output.Artifacts[1].Digest {
		t.Fatalf("migration artifact = %#v", response.Output.Artifacts[1])
	}
	if response.Output.Artifacts[2].Path != "generated/data/database.primary/schema/schema.sql" || !reflect.DeepEqual(response.Output.Artifacts[2].OwningMembers, []string{"accounts.user/v1"}) {
		t.Fatalf("schema artifact = %#v", response.Output.Artifacts[2])
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitRejectsChangedFrozenInputs(t *testing.T) {
	base := validEmitRequest(t)
	tests := []struct {
		name string
		edit func(*protocol.EmitRequest)
	}{
		{"input digest", func(r *protocol.EmitRequest) { r.InputDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"analyze digest", func(r *protocol.EmitRequest) {
			r.AnalyzeDigest = "sha256:" + strings.Repeat("0", 64)
			refreshEmitDigest(t, r)
		}},
		{"analyze roots", func(r *protocol.EmitRequest) { r.Analyze.Roots[0].Model.Namespace = "changed" }},
		{"frozen model", func(r *protocol.EmitRequest) { r.FrozenModelDigest = "sha256:" + strings.Repeat("3", 64) }},
		{"build", func(r *protocol.EmitRequest) { r.Build.GOOS = "linux" }},
		{"compiler", func(r *protocol.EmitRequest) { r.Compiler.ModuleVersion = "v9.9.9" }},
		{"truncated analysis", func(r *protocol.EmitRequest) { r.Analyze.Truncated = true; refreshEmitDigest(t, r) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Analyze.Roots = append([]analyzer.Root(nil), base.Analyze.Roots...)
			test.edit(&request)
			response := protocol.HandleEmit(request)
			if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 || response.OutputDigest != "" || len(response.Diagnostics) != 1 {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestEmitRejectsUnsafeRootAndUnsupportedBackend(t *testing.T) {
	tests := []struct {
		name       string
		assignment protocol.EmitAssignment
		code       analyzer.DiagnosticCode
	}{
		{"traversal", protocol.EmitAssignment{AllowedRoot: "generated/data/database.primary/../../outside"}, analyzer.CodeEmitPath},
		{"absolute", protocol.EmitAssignment{AllowedRoot: "C:/outside"}, analyzer.CodeEmitPath},
		{"unselected root", protocol.EmitAssignment{AllowedRoot: "generated/data/database.other"}, analyzer.CodeEmitPath},
		{"backend", protocol.EmitAssignment{Backend: "sqlite/v1"}, analyzer.CodeEmitBackend},
		{"provider", protocol.EmitAssignment{Provider: ""}, analyzer.CodeEmitBackend},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validEmitRequest(t)
			assignment := &request.Assignments[0]
			if test.assignment.AllowedRoot != "" {
				assignment.AllowedRoot = test.assignment.AllowedRoot
			}
			if test.name == "backend" {
				assignment.Backend = test.assignment.Backend
			}
			if test.name == "provider" {
				assignment.Provider = ""
			}
			refreshEmitDigest(t, &request)
			response := protocol.HandleEmit(request)
			if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 || len(response.Diagnostics) != 1 || response.Diagnostics[0].Code != test.code {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestEmitGroupsMembersByResourceAndRequiresExplicitImports(t *testing.T) {
	request := validEmitRequest(t)
	second := analyzer.Root{MemberID: "audit.events/v1", Model: normalized(t, declaration.Member[struct{}]{Namespace: "audit", Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "pk-events", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}}}}), MigrationOnly: true}
	request.Analyze.Roots = append(request.Analyze.Roots, second)
	request.Analyze.Roots[0].Model.Imports = []string{second.MemberID}
	request.Analyze.Roots[0].Model, _ = (declaration.Member[struct{}]{Namespace: request.Analyze.Roots[0].Model.Namespace, Imports: request.Analyze.Roots[0].Model.Imports, Tables: request.Analyze.Roots[0].Model.Tables, Migrations: request.Analyze.Roots[0].Model.Migrations}).Normalize()
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	if response := protocol.HandleEmit(request); response.Status != protocol.StatusRejected || response.Diagnostics[0].Code != analyzer.CodeEmitBackend {
		t.Fatalf("inactive import response = %#v", response)
	}
	request.Assignments = append(request.Assignments, protocol.EmitAssignment{MemberID: second.MemberID, Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "generated/data/database.primary"})
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Artifacts) != 3 || !reflect.DeepEqual(response.Output.Artifacts[2].OwningMembers, []string{"accounts.user/v1", "audit.events/v1"}) {
		t.Fatalf("shared Resource response = %#v", response)
	}
	request.Assignments[1].Resource = "database.audit"
	request.Assignments[1].AllowedRoot = "generated/data/database.audit"
	refreshEmitDigest(t, &request)
	if response := protocol.HandleEmit(request); response.Status != protocol.StatusRejected || response.Diagnostics[0].Code != analyzer.CodeEmitBackend {
		t.Fatalf("cross-Resource import response = %#v", response)
	}
}

func TestEmitOutputIsDeterministicAcrossAssignmentOrder(t *testing.T) {
	request := validEmitRequest(t)
	second := analyzer.Root{MemberID: "audit.events/v1", Model: normalized(t, declaration.Member[struct{}]{Namespace: "audit", Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}}}), MigrationOnly: true}
	request.Analyze.Roots = append(request.Analyze.Roots, second)
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	request.Assignments = append(request.Assignments, protocol.EmitAssignment{MemberID: second.MemberID, Resource: "database.audit", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "generated/data/database.audit"})
	refreshEmitDigest(t, &request)
	first := protocol.HandleEmit(request)
	if first.Status != protocol.StatusSucceeded || len(first.Output.Artifacts) != 5 {
		t.Fatalf("first response = %#v", first)
	}
	request.Assignments[0], request.Assignments[1] = request.Assignments[1], request.Assignments[0]
	refreshEmitDigest(t, &request)
	secondResponse := protocol.HandleEmit(request)
	if secondResponse.Status != protocol.StatusSucceeded || first.InputDigest != secondResponse.InputDigest || first.OutputDigest != secondResponse.OutputDigest || !reflect.DeepEqual(first.Output.Artifacts, secondResponse.Output.Artifacts) {
		t.Fatalf("assignment order changed output: first=%#v second=%#v", first, secondResponse)
	}
}

func TestValidateEmitResponseRejectsTampering(t *testing.T) {
	request := validEmitRequest(t)
	base := protocol.HandleEmit(request)
	if base.Status != protocol.StatusSucceeded {
		t.Fatalf("base = %#v", base)
	}
	tests := []struct {
		name string
		edit func(*protocol.EmitResponse)
	}{
		{"content", func(r *protocol.EmitResponse) { r.Output.Artifacts[0].Bytes = []byte("changed") }},
		{"path traversal", func(r *protocol.EmitResponse) {
			r.Output.Artifacts[0].Path = "generated/data/database.primary/../outside"
		}},
		{"mode", func(r *protocol.EmitResponse) { r.Output.Artifacts[0].Mode = 0755 }},
		{"owner", func(r *protocol.EmitResponse) { r.Output.Artifacts[0].OwningMembers = []string{"other.member/v1"} }},
		{"resource", func(r *protocol.EmitResponse) { r.Output.Artifacts[0].Resource = "database.other" }},
		{"backend", func(r *protocol.EmitResponse) { r.Output.Artifacts[0].Backend = "sqlite/v1" }},
		{"compiler", func(r *protocol.EmitResponse) { r.Output.Artifacts[0].Compiler.ModuleVersion = "v2" }},
		{"output digest", func(r *protocol.EmitResponse) { r.OutputDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"duplicate", func(r *protocol.EmitResponse) { r.Output.Artifacts[1].Path = r.Output.Artifacts[0].Path }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := base
			response.Output.Artifacts = append([]protocol.StagedArtifact(nil), base.Output.Artifacts...)
			test.edit(&response)
			if err := protocol.ValidateEmitResponse(request, response); err == nil {
				t.Fatal("tampered response accepted")
			}
		})
	}
	t.Run("self-consistent manifest tamper", func(t *testing.T) {
		response := base
		response.Output.Artifacts = append([]protocol.StagedArtifact(nil), base.Output.Artifacts...)
		manifest := &response.Output.Artifacts[0]
		manifest.Bytes = bytes.Replace(manifest.Bytes, []byte("plystra.data-instance-manifest/v1"), []byte("invalid-manifest/v1"), 1)
		manifest.Digest = postgres.DigestSQL(string(manifest.Bytes))
		payload, err := json.Marshal(response.Output.Artifacts)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(append([]byte("plystra.data.emit-output/v1\x00"), payload...))
		response.OutputDigest = "sha256:" + hex.EncodeToString(sum[:])
		if err := protocol.ValidateEmitResponse(request, response); err == nil {
			t.Fatal("self-consistent but invalid manifest accepted")
		}
	})
}

func TestEmitRejectsOversizedArtifactWithoutPartialOutput(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Tables[0].Columns[0].Type = declaration.TypeString
	model.Tables[0].Columns[0].Default = declaration.Value{Set: true, Kind: declaration.TypeString, String: strings.Repeat("a", protocol.MaxArtifactBytes)}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{Namespace: model.Namespace, Tables: model.Tables, Migrations: model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 || response.Diagnostics[0].Code != analyzer.CodeLimitExceeded {
		t.Fatalf("oversized artifact response = %#v", response)
	}
}

func validEmitRequest(t *testing.T) protocol.EmitRequest {
	t.Helper()
	model := normalized(t, declaration.Member[struct{}]{Namespace: "accounts", Tables: []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "pk-accounts", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}}}, Migrations: []declaration.Migration{{ID: "create-accounts", Version: 1, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "accounts"}}}}}})
	roots := []analyzer.Root{{MemberID: "accounts.user/v1", Model: model, MigrationOnly: true}}
	resultDigest, err := analyzer.ResultDigest(roots, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "emit-1",
		Compiler: validRequest(t).Compiler, Build: protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"},
		Analyze: protocol.AnalyzeOutput{Roots: roots, Digest: resultDigest, Valid: true}, AnalyzeDigest: resultDigest,
		FrozenModelDigest: "sha256:" + strings.Repeat("4", 64),
		Assignments:       []protocol.EmitAssignment{{MemberID: "accounts.user/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "generated/data/database.primary"}},
	}
	refreshEmitDigest(t, &request)
	return request
}

func normalized(t *testing.T, member declaration.Member[struct{}]) declaration.Model {
	t.Helper()
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func refreshEmitDigest(t *testing.T, request *protocol.EmitRequest) {
	t.Helper()
	digest, err := protocol.EmitInputDigest(*request)
	if err != nil {
		t.Fatal(err)
	}
	request.InputDigest = digest
}
