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
	"github.com/plystra/data/migration"
	postgres "github.com/plystra/data/postgres/compiler"
	"github.com/plystra/data/protocol"
)

func TestCompilerCommandRetainsHistoricalMigrationArtifact(t *testing.T) {
	table := declaration.Table{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}
	model, err := (declaration.Member[struct{}]{Namespace: "accounts", Tables: []declaration.Table{table}, Migrations: []declaration.Migration{{
		ID: "create", Version: 1,
		Snapshot:   []declaration.MigrationSchema{{Namespace: "accounts", Tables: []declaration.Table{table}}},
		Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "accounts"}}},
	}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	retained, err := postgres.CompileMigrations([]postgres.SchemaMember{{MemberID: "accounts.user/v1", Resource: "database.primary", Model: model}}, "compiler/old", postgres.MigrationHistory{})
	if err != nil {
		t.Fatal(err)
	}
	retained[0].SQL += "-- exact old compiler SQL\n"
	retained[0].Definition.ArtifactDigest = postgres.DigestSQL(retained[0].SQL)
	d := retained[0].Definition
	roots := []analyzer.Root{{MemberID: "accounts.user/v1", Model: model, MigrationOnly: true, AccessPackage: "github.com/plystra/data/declaration", AccessType: "NoAccess"}}
	analyzeDigest, err := analyzer.ResultDigest(roots, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "migration-command",
		Compiler: protocol.CompilerIdentity{ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)), ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26"},
		Build:    protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"}, Analyze: protocol.AnalyzeOutput{Roots: roots, Digest: analyzeDigest, Valid: true}, AnalyzeDigest: analyzeDigest, FrozenModelDigest: "sha256:" + strings.Repeat("3", 64),
		Assignments:      []protocol.EmitAssignment{{MemberID: "accounts.user/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "dataaccess/database.primary"}},
		MigrationHistory: []protocol.EmitMigrationHistory{{Resource: "database.primary", History: postgres.MigrationHistory{Artifacts: retained, Applied: []migration.HistoryEntry{{Identity: d.Identity, Version: d.Version, LogicalDigest: d.LogicalDigest, ArtifactDigest: d.ArtifactDigest, Compiler: d.Compiler, Backend: d.Backend}}}}},
	}
	request.InputDigest, err = protocol.EmitInputDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []bool{false, true} {
		if corrupt {
			request.MigrationHistory[0].History.Artifacts = nil
			request.InputDigest, _ = protocol.EmitInputDigest(request)
		}
		var input, stdout, stderr bytes.Buffer
		if err := protocol.WriteFrame(&input, request); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		command := exec.CommandContext(ctx, "go", "run", ".")
		command.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
		command.Stdin, command.Stdout, command.Stderr = &input, &stdout, &stderr
		err := command.Run()
		cancel()
		if err != nil || stderr.Len() != 0 {
			t.Fatalf("compiler command = %v, stderr=%s", err, stderr.String())
		}
		var response protocol.EmitResponse
		if err := protocol.ReadFrame(&stdout, &response); err != nil || stdout.Len() != 0 {
			t.Fatalf("compiler frame = %v, trailing bytes %d", err, stdout.Len())
		}
		if corrupt {
			if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 {
				t.Fatal("compiler reconstructed missing historical artifact")
			}
			continue
		}
		if response.Status != protocol.StatusSucceeded || protocol.ValidateEmitResponse(request, response) != nil {
			t.Fatalf("compiler response = %#v", response.Diagnostics)
		}
		found := false
		for _, artifact := range response.Output.Artifacts {
			if artifact.Migration != nil {
				found = true
				if string(artifact.Bytes) != retained[0].SQL || artifact.Migration.Compiler != d.Compiler || artifact.Migration.ArtifactDigest != d.ArtifactDigest {
					t.Fatal("compiler replaced immutable history")
				}
			}
		}
		if !found {
			t.Fatal("compiler omitted retained history")
		}
	}
}
