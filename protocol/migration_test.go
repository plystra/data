package protocol_test

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/migration"
	postgres "github.com/plystra/data/postgres/compiler"
	"github.com/plystra/data/protocol"
)

func TestEmitRetainsHistoricalMigrationAcrossCompilerUpgrade(t *testing.T) {
	request, retained := historicalEmitRequest(t)
	var input, output bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	if err := protocol.Run(&input, &output); err != nil {
		t.Fatal(err)
	}
	var response protocol.EmitResponse
	if err := protocol.ReadFrame(&output, &response); err != nil || output.Len() != 0 {
		t.Fatalf("response frame = %v, trailing %d", err, output.Len())
	}
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("emit rejected retained history: %#v", response.Diagnostics)
	}
	found := false
	for _, artifact := range response.Output.Artifacts {
		if artifact.Migration != nil {
			found = true
			if !reflect.DeepEqual(*artifact.Migration, retained.Definition) || string(artifact.Bytes) != retained.SQL {
				t.Fatalf("historical artifact replaced: %#v", artifact)
			}
		}
	}
	if !found {
		t.Fatal("retained migration missing")
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
	// Reconstructed output from the new compiler cannot substitute for retained
	// bytes, even when all caller-controlled response checksums are refreshed.
	fresh := request
	fresh.MigrationHistory = nil
	refreshEmitDigest(t, &fresh)
	rebuilt := protocol.HandleEmit(fresh)
	rebuilt.InputDigest = request.InputDigest
	if err := protocol.ValidateEmitResponse(request, rebuilt); err == nil {
		t.Fatal("reconstructed migration accepted in place of history")
	}
}

func TestEmitRejectsMissingCorruptOrRelabeledMigrationHistory(t *testing.T) {
	tests := []struct {
		name   string
		change func(*protocol.EmitRequest)
	}{
		{"missing retained bytes", func(r *protocol.EmitRequest) { r.MigrationHistory[0].History.Artifacts = nil }},
		{"corrupt bytes", func(r *protocol.EmitRequest) { r.MigrationHistory[0].History.Artifacts[0].SQL += "bad" }},
		{"relabel compiler", func(r *protocol.EmitRequest) {
			r.MigrationHistory[0].History.Artifacts[0].Definition.Compiler = "new/compiler"
		}},
		{"changed logical snapshot", func(r *protocol.EmitRequest) {
			r.Analyze.Roots[0].Model.Migrations[0].Snapshot[0].Namespace = "changed"
			r.Analyze.Digest, _ = analyzer.ResultDigest(r.Analyze.Roots, nil, false)
			r.AnalyzeDigest = r.Analyze.Digest
		}},
		{"wrong Resource", func(r *protocol.EmitRequest) { r.MigrationHistory[0].Resource = "database.other" }},
		{"duplicate Resource", func(r *protocol.EmitRequest) { r.MigrationHistory = append(r.MigrationHistory, r.MigrationHistory[0]) }},
		{"wrong owner", func(r *protocol.EmitRequest) {
			r.MigrationHistory[0].History.Artifacts[0].Definition.Identity.Member = "other.data/v1"
		}},
		{"deleted source", func(r *protocol.EmitRequest) {
			r.Analyze.Roots[0].Model.Migrations = nil
			r.Analyze.Digest, _ = analyzer.ResultDigest(r.Analyze.Roots, nil, false)
			r.AnalyzeDigest = r.Analyze.Digest
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, _ := historicalEmitRequest(t)
			test.change(&request)
			refreshEmitDigest(t, &request)
			response := protocol.HandleEmit(request)
			if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 {
				t.Fatalf("invalid history emitted partial artifacts: %#v", response)
			}
		})
	}
}

func TestEmitInputDigestBindsRetainedArtifactsAndObservedOrder(t *testing.T) {
	request, _ := historicalEmitRequest(t)
	original := request.InputDigest
	request.MigrationHistory[0].History.Applied[0].Compiler = "changed"
	changed, _ := protocol.EmitInputDigest(request)
	if changed == original {
		t.Fatal("observed history omitted from emit digest")
	}
	request, retained := historicalEmitRequest(t)
	second := retained
	second.Definition.Identity.Name = "second"
	request.MigrationHistory[0].History.Artifacts = append(request.MigrationHistory[0].History.Artifacts, second)
	ordered, _ := protocol.EmitInputDigest(request)
	request.MigrationHistory[0].History.Artifacts[0], request.MigrationHistory[0].History.Artifacts[1] = request.MigrationHistory[0].History.Artifacts[1], request.MigrationHistory[0].History.Artifacts[0]
	reversed, _ := protocol.EmitInputDigest(request)
	if ordered != reversed {
		t.Fatal("artifact inventory order changed emit digest")
	}
	request.MigrationHistory[0].History.Applied = append(request.MigrationHistory[0].History.Applied, migration.HistoryEntry{Identity: second.Definition.Identity})
	ordered, _ = protocol.EmitInputDigest(request)
	request.MigrationHistory[0].History.Applied[0], request.MigrationHistory[0].History.Applied[1] = request.MigrationHistory[0].History.Applied[1], request.MigrationHistory[0].History.Applied[0]
	reversed, _ = protocol.EmitInputDigest(request)
	if ordered == reversed {
		t.Fatal("applied history order omitted from emit digest")
	}
}

func historicalEmitRequest(t *testing.T) (protocol.EmitRequest, postgres.Artifact) {
	t.Helper()
	request := validEmitRequest(t)
	initial := protocol.HandleEmit(request)
	if initial.Status != protocol.StatusSucceeded {
		t.Fatalf("initial emit = %#v", initial.Diagnostics)
	}
	var retained postgres.Artifact
	for _, artifact := range initial.Output.Artifacts {
		if artifact.Migration != nil {
			retained = postgres.Artifact{Definition: *artifact.Migration, SQL: string(artifact.Bytes) + "-- retained old compiler output\n"}
			retained.Definition.ArtifactDigest = postgres.DigestSQL(retained.SQL)
		}
	}
	d := retained.Definition
	history := postgres.MigrationHistory{Artifacts: []postgres.Artifact{retained}, Applied: []migration.HistoryEntry{{Identity: d.Identity, Version: d.Version, LogicalDigest: d.LogicalDigest, ArtifactDigest: d.ArtifactDigest, Compiler: d.Compiler, Backend: d.Backend}}}
	request.MigrationHistory = []protocol.EmitMigrationHistory{{Resource: "database.primary", History: history}}
	request.Compiler.ModuleVersion = "v1.2.4"
	request.Analyze.Roots[0].Model.Tables[0].Columns = append(request.Analyze.Roots[0].Model.Tables[0].Columns, declaration.Column{ID: "email", Type: declaration.TypeString, Nullable: true})
	model := request.Analyze.Roots[0].Model
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{Namespace: model.Namespace, Tables: model.Tables, Migrations: model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	return request, retained
}
