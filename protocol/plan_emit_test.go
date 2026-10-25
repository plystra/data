package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
	postgres "github.com/plystra/data/postgres/compiler"
	"github.com/plystra/data/protocol"
)

func TestEmitBindsAtomicPlanInputsResultsAndMutationAssertions(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{
		{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
			Projection: []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
			Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}},
		{ID: "remove", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "accounts"},
			Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
			Predicate:  declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}},
	}
	model.Plans = []declaration.Plan{{ID: "remove-plan", Parameters: []declaration.Parameter{{ID: "account-id", Type: declaration.TypeInt64}}, Statements: []declaration.PlanStatement{
		{ID: "read", Query: declaration.Ref{Symbol: "read"}, Cardinality: declaration.CardinalityExactlyOne, Bindings: []declaration.PlanBinding{{Parameter: "id", FromParameter: "account-id"}}},
		{ID: "remove", Query: declaration.Ref{Symbol: "remove"}, Cardinality: declaration.CardinalityNone, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromStatement: "read", FromResult: "id"}}},
	}}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Plans: model.Plans, Migrations: model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	base := protocol.HandleEmit(request)
	if base.Status != protocol.StatusSucceeded {
		t.Fatalf("emit = %#v", base.Diagnostics)
	}
	if err := protocol.ValidateEmitResponse(request, base); err != nil {
		t.Fatal(err)
	}
	var metadata *protocol.PlanMetadata
	for _, artifact := range base.Output.Artifacts {
		if artifact.Plan != nil {
			metadata = artifact.Plan
		}
	}
	if metadata == nil || len(metadata.Parameters) != 1 || metadata.Parameters[0].ID != "account-id" || len(metadata.Statements) != 2 {
		t.Fatalf("plan metadata = %#v", metadata)
	}
	if metadata.Statements[0].Args[0].FromInput.ID != "account-id" || metadata.Statements[1].Args[0].FromResult.Statement != "read" || metadata.Statements[1].Affected != database.ExactAffectedRows(1) || metadata.Statements[1].ReturnsRows {
		t.Fatal("staged plan lost execution bindings or assertions")
	}
	for _, edit := range []func(*protocol.PlanMetadata){
		func(plan *protocol.PlanMetadata) { plan.Parameters[0].ID = "other-id" },
		func(plan *protocol.PlanMetadata) { plan.Statements[0].Args[0].FromInput.ID = "other-id" },
		func(plan *protocol.PlanMetadata) { plan.Statements[1].Args[0].FromResult.Column = 1 },
		func(plan *protocol.PlanMetadata) { plan.Statements[1].Affected.Min = 0 },
		func(plan *protocol.PlanMetadata) { plan.Statements[1].ReturnsRows = true },
	} {
		payload, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		var response protocol.EmitResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatal(err)
		}
		for index := range response.Output.Artifacts {
			artifact := &response.Output.Artifacts[index]
			if artifact.Plan == nil {
				continue
			}
			edit(artifact.Plan)
			artifact.Bytes, err = json.Marshal(artifact.Plan)
			if err != nil {
				t.Fatal(err)
			}
			artifact.Digest = postgres.DigestSQL(string(artifact.Bytes))
		}
		refreshEmitOutputDigest(t, &response)
		if err := protocol.ValidateEmitResponse(request, response); err == nil {
			t.Fatal("rehashing changed plan bindings or assertions bypassed accepted intent")
		}
	}
}
