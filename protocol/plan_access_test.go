package protocol_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	postgres "github.com/plystra/data/postgres/compiler"
	"github.com/plystra/data/protocol"
)

func TestEmitCallableAtomicPlanProviderAndTamperValidation(t *testing.T) {
	request := typedAccessEmitRequest(t)
	root := &request.Analyze.Roots[0]
	query := root.Model.Queries[0]
	query.Method, query.Cardinality = "", ""
	root.Model.Queries = []declaration.Query{query, {
		ID: "touch", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"},
		Parameters:  []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
		Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}},
		Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}},
		Returning:   []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}}
	root.Model.Plans = []declaration.Plan{{ID: "atomic-get", Method: "Get", Parameters: []declaration.Parameter{{ID: "account-id", GoField: "Key", Type: declaration.TypeInt64}}, Statements: []declaration.PlanStatement{
		{ID: "read", Query: declaration.Ref{Symbol: "get"}, Cardinality: declaration.CardinalityExactlyOne, Bindings: []declaration.PlanBinding{{Parameter: "id", FromParameter: "account-id"}}},
		{ID: "touch", Query: declaration.Ref{Symbol: "touch"}, Cardinality: declaration.CardinalityExactlyOne, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromStatement: "read", FromResult: "id"}}},
	}, Outputs: []declaration.PlanOutput{{Statement: "touch", Result: "id", GoField: "Key"}}}}
	root.Model = normalized(t, declaration.Member[struct{}]{Namespace: root.Model.Namespace, Tables: root.Model.Tables, Queries: root.Model.Queries, Plans: root.Model.Plans, Migrations: root.Model.Migrations})
	root.QueryMethods = nil
	root.PlanMethods = []analyzer.PlanMethod{{Name: "Get", RequestPackage: root.AccessPackage, RequestType: "Request", ResultPackage: root.AccessPackage, ResultType: "Row"}}
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	var input, output bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	if err := protocol.Run(&input, &output); err != nil {
		t.Fatal(err)
	}
	var response protocol.EmitResponse
	if err := protocol.ReadFrame(&output, &response); err != nil || response.Status != protocol.StatusSucceeded || output.Len() != 0 {
		t.Fatalf("plan provider emit = %#v, %v", response.Diagnostics, err)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
	var path, before, after string
	for index := range response.Output.Artifacts {
		artifact := &response.Output.Artifacts[index]
		if !strings.HasSuffix(artifact.Path, "/provider.go") {
			continue
		}
		if !bytes.Contains(artifact.Bytes, []byte(`db.InputArgument(db.ValueKind("int64"), "account-id")`)) || !bytes.Contains(artifact.Bytes, []byte(`db.ResultArgument(db.ValueKind("int64"), "read", 0)`)) {
			t.Fatal("generated provider lost plan bindings")
		}
		path, before = artifact.Path, artifact.Digest
		artifact.Bytes = bytes.ReplaceAll(artifact.Bytes, []byte(`db.ResultArgument(db.ValueKind("int64"), "read", 0)`), []byte(`db.InputArgument(db.ValueKind("int64"), "account-id")`))
		artifact.Digest = postgres.DigestSQL(string(artifact.Bytes))
		after = artifact.Digest
	}
	if path == "" || before == after {
		t.Fatal("fixture did not change compiled plan source")
	}
	for index := range response.Output.Artifacts {
		artifact := &response.Output.Artifacts[index]
		if !strings.HasSuffix(artifact.Path, "/manifest.json") {
			continue
		}
		var manifest map[string]json.RawMessage
		if err := json.Unmarshal(artifact.Bytes, &manifest); err != nil {
			t.Fatal(err)
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(manifest["artifacts"], &entries); err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			var entryPath string
			if err := json.Unmarshal(entry["path"], &entryPath); err != nil {
				t.Fatal(err)
			}
			if entryPath == path {
				entry["digest"], _ = json.Marshal(after)
			}
		}
		manifest["artifacts"], _ = json.Marshal(entries)
		artifact.Bytes, _ = json.Marshal(manifest)
		artifact.Digest = postgres.DigestSQL(string(artifact.Bytes))
	}
	refreshEmitOutputDigest(t, &response)
	if err := protocol.ValidateEmitResponse(request, response); err == nil {
		t.Fatal("self-consistent plan provider binding tamper accepted")
	}
}
