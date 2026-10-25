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
	"github.com/plystra/data/migration"
	postgres "github.com/plystra/data/postgres/compiler"
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
		if !strings.HasPrefix(artifact.Path, "dataaccess/database.primary/") || artifact.Mode != 0644 || artifact.Digest != postgres.DigestSQL(string(artifact.Bytes)) || artifact.Backend != "postgres/v1" || artifact.Resource != "database.primary" || artifact.Compiler != request.Compiler || artifact.FrozenModelDigest != request.FrozenModelDigest {
			t.Fatalf("invalid artifact = %#v", artifact)
		}
	}
	if response.Output.Artifacts[0].Path != "dataaccess/database.primary/manifest.json" || !bytes.Contains(response.Output.Artifacts[0].Bytes, []byte("plystra.data-instance-manifest/v1")) {
		t.Fatalf("manifest artifact = %#v", response.Output.Artifacts[0])
	}
	if response.Output.Artifacts[1].Path != "dataaccess/database.primary/migrations/accounts.user/v1/create-accounts.sql" || response.Output.Artifacts[1].Migration == nil || response.Output.Artifacts[1].Migration.ArtifactDigest != response.Output.Artifacts[1].Digest {
		t.Fatalf("migration artifact = %#v", response.Output.Artifacts[1])
	}
	var manifest struct {
		Artifacts []struct {
			Path      string                `json:"path"`
			Migration *migration.Definition `json:"migration,omitempty"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(response.Output.Artifacts[0].Bytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Artifacts) != 2 || manifest.Artifacts[0].Migration == nil || !reflect.DeepEqual(manifest.Artifacts[0].Migration, response.Output.Artifacts[1].Migration) {
		t.Fatalf("manifest migration provenance = %#v", manifest.Artifacts)
	}
	if response.Output.Artifacts[2].Path != "dataaccess/database.primary/schema/schema.sql" || !reflect.DeepEqual(response.Output.Artifacts[2].OwningMembers, []string{"accounts.user/v1"}) {
		t.Fatalf("schema artifact = %#v", response.Output.Artifacts[2])
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitIncludesOwnedQueryArtifactAndManifestEntry(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "list-accounts", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Projection: []declaration.Projection{{Column: "id"}},
		Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Artifacts) != 4 {
		t.Fatalf("response = %#v", response)
	}
	var query protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Path == "dataaccess/database.primary/queries/accounts.user/v1/list-accounts.sql" {
			query = artifact
		}
	}
	if query.Path == "" || query.Migration != nil || !bytes.Contains(query.Bytes, []byte(`SELECT "id" FROM "accounts"."accounts";`)) || !reflect.DeepEqual(query.OwningMembers, []string{"accounts.user/v1"}) {
		t.Fatalf("query artifact = %#v", query)
	}
	if query.Query == nil || query.Query.ID != "list-accounts" || query.Query.Operation != declaration.OperationSelect || len(query.Query.Parameters) != 0 || !reflect.DeepEqual(query.Query.Results, model.Queries[0].Results) {
		t.Fatalf("query metadata = %#v", query.Query)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
	response.Output.Artifacts = append([]protocol.StagedArtifact(nil), response.Output.Artifacts...)
	for index := range response.Output.Artifacts {
		if response.Output.Artifacts[index].Path == query.Path {
			response.Output.Artifacts[index].OwningMembers = []string{"other.member/v1"}
		}
	}
	if err := protocol.ValidateEmitResponse(request, response); err == nil {
		t.Fatal("query with an unrelated owner was accepted")
	}
}

func TestEmitTypedAccessProviderAndTamperValidation(t *testing.T) {
	request := typedAccessEmitRequest(t)
	var input, output bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	if err := protocol.Run(&input, &output); err != nil {
		t.Fatal(err)
	}
	var response protocol.EmitResponse
	if err := protocol.ReadFrame(&output, &response); err != nil || output.Len() != 0 {
		t.Fatalf("emit frame = %v, trailing bytes %d", err, output.Len())
	}
	if response.Status != protocol.StatusSucceeded || len(response.Output.Artifacts) != 5 {
		t.Fatalf("response = %#v", response)
	}
	const providerPath = "dataaccess/database.primary/providers/accounts.user/v1/provider.go"
	var provider protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Path == providerPath {
			provider = artifact
		}
	}
	if provider.Path == "" || !bytes.Contains(provider.Bytes, []byte("//plystra:implements-resource accounts.access/v1")) || provider.Query != nil || provider.Plan != nil || provider.Migration != nil || !reflect.DeepEqual(provider.OwningMembers, []string{"accounts.user/v1"}) {
		t.Fatalf("provider = %#v", provider)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
	// Rehash both manifest and response; intent validation must still catch it.
	originalDigest := provider.Digest
	for index := range response.Output.Artifacts {
		artifact := &response.Output.Artifacts[index]
		if artifact.Path == providerPath {
			artifact.Bytes = bytes.ReplaceAll(artifact.Bytes, []byte("return &Provider{database: database}, nil"), []byte("return nil, nil"))
			artifact.Digest = postgres.DigestSQL(string(artifact.Bytes))
			provider = *artifact
		}
	}
	if provider.Digest == originalDigest {
		t.Fatal("fixture did not alter source")
	}
	for index := range response.Output.Artifacts {
		artifact := &response.Output.Artifacts[index]
		if strings.HasSuffix(artifact.Path, "/manifest.json") {
			var manifest map[string]json.RawMessage
			if err := json.Unmarshal(artifact.Bytes, &manifest); err != nil {
				t.Fatal(err)
			}
			var entries []map[string]json.RawMessage
			if err := json.Unmarshal(manifest["artifacts"], &entries); err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				var path string
				_ = json.Unmarshal(entry["path"], &path)
				if path == providerPath {
					entry["digest"], _ = json.Marshal(provider.Digest)
				}
			}
			manifest["artifacts"], _ = json.Marshal(entries)
			artifact.Bytes, _ = json.Marshal(manifest)
			artifact.Digest = postgres.DigestSQL(string(artifact.Bytes))
		}
	}
	refreshEmitOutputDigest(t, &response)
	if err := protocol.ValidateEmitResponse(request, response); err == nil {
		t.Fatal("self-consistent provider source tamper accepted")
	}
}

func TestEmitRejectsUnsupportedAccessResourceWithoutPartialArtifacts(t *testing.T) {
	request := typedAccessEmitRequest(t)
	root := &request.Analyze.Roots[0]
	query := &root.Model.Queries[0]
	query.Operation = declaration.OperationDelete
	query.Returning, query.Projection = query.Projection, nil
	root.Model = normalized(t, declaration.Member[struct{}]{Namespace: root.Model.Namespace, Tables: root.Model.Tables, Queries: root.Model.Queries, Migrations: root.Model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 || len(response.Diagnostics) != 1 || response.Diagnostics[0].Code != analyzer.CodeEmitBackend {
		t.Fatalf("partial unsupported provider: %#v", response)
	}
}

func typedAccessEmitRequest(t *testing.T) protocol.EmitRequest {
	t.Helper()
	request := validEmitRequest(t)
	root := &request.Analyze.Roots[0]
	root.MigrationOnly = false
	root.AccessID, root.AccessPackage, root.AccessType = "accounts.access/v1", "example.com/accounts", "Resource"
	root.Model = normalized(t, declaration.Member[struct{}]{Namespace: root.Model.Namespace, Tables: root.Model.Tables, Migrations: root.Model.Migrations, Queries: []declaration.Query{{
		ID: "get", Method: "Get", Cardinality: declaration.CardinalityExactlyOne, Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Parameters: []declaration.Parameter{{ID: "id", GoField: "Key", Type: declaration.TypeInt64}},
		Predicate:  declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
		Projection: []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", GoField: "Key", Source: "id", Type: declaration.TypeInt64}},
	}}})
	root.QueryMethods = []analyzer.QueryMethod{{Name: "Get", Cardinality: declaration.CardinalityExactlyOne, RequestPackage: root.AccessPackage, RequestType: "Request", RowPackage: root.AccessPackage, RowType: "Row"}}
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	return request
}

func TestEmitIncludesTypedMutationQueryArtifact(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "update-account", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"},
		Parameters:  []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
		Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}}},
		Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Artifacts) != 4 {
		t.Fatalf("response = %#v", response)
	}
	var query protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Path == "dataaccess/database.primary/queries/accounts.user/v1/update-account.sql" {
			query = artifact
		}
	}
	if query.Path == "" || !bytes.Contains(query.Bytes, []byte(`UPDATE "accounts"."accounts" SET "id" = $1 WHERE "id" = $1;`)) || query.Migration != nil {
		t.Fatalf("query artifact = %#v", query)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitIncludesConflictMetadataForInsert(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "insert-account", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "accounts"},
		Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Int64(1)}},
		Conflict:    declaration.Conflict{Constraint: "pk-accounts", Action: declaration.ConflictDoNothing},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("response = %#v", response)
	}
	var query protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if strings.HasSuffix(artifact.Path, "/insert-account.sql") {
			query = artifact
		}
	}
	if query.Query == nil || query.Query.Conflict == nil || query.Query.Conflict.Constraint != "pk-accounts" || !bytes.Contains(query.Bytes, []byte(`ON CONFLICT ON CONSTRAINT "pk-accounts" DO NOTHING`)) {
		t.Fatalf("conflict artifact = %#v", query)
	}
	for _, artifact := range response.Output.Artifacts {
		if !strings.HasSuffix(artifact.Path, "/manifest.json") {
			continue
		}
		var manifest struct {
			Artifacts []struct {
				Path  string                  `json:"path"`
				Query *protocol.QueryMetadata `json:"query"`
			} `json:"artifacts"`
		}
		if err := json.Unmarshal(artifact.Bytes, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, entry := range manifest.Artifacts {
			if entry.Path == query.Path && !reflect.DeepEqual(entry.Query, query.Query) {
				t.Fatalf("manifest conflict metadata = %#v, query metadata = %#v", entry.Query, query.Query)
			}
		}
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitConflictUpdateMetadataAndTamperValidation(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Tables[0].Columns = append(model.Tables[0].Columns, declaration.Column{ID: "email", Type: declaration.TypeString})
	model.Queries = []declaration.Query{{
		ID: "upsert-account", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "accounts"},
		Parameters:  []declaration.Parameter{{ID: "email", Type: declaration.TypeString}},
		Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Int64(1)}, {Column: "email", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "email"}}},
		Conflict:    declaration.Conflict{Constraint: "pk-accounts", Action: declaration.ConflictDoUpdate, UpdateColumns: []string{"email"}},
		Returning:   []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("response = %#v", response)
	}
	var query *protocol.StagedArtifact
	for index := range response.Output.Artifacts {
		if strings.HasSuffix(response.Output.Artifacts[index].Path, "/upsert-account.sql") {
			query = &response.Output.Artifacts[index]
		}
	}
	if query == nil || query.Query == nil || query.Query.Conflict == nil || !reflect.DeepEqual(query.Query.Conflict.UpdateColumns, []string{"email"}) || !bytes.Contains(query.Bytes, []byte(`DO UPDATE SET "email" = EXCLUDED."email" RETURNING "id";`)) {
		t.Fatalf("upsert artifact = %#v", query)
	}
	var manifest struct {
		Artifacts []struct {
			Path  string                  `json:"path"`
			Query *protocol.QueryMetadata `json:"query"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(response.Output.Artifacts[0].Bytes, &manifest); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range manifest.Artifacts {
		if entry.Path == query.Path {
			found = reflect.DeepEqual(entry.Query, query.Query)
		}
	}
	if !found {
		t.Fatalf("manifest has no matching upsert metadata: %#v", manifest)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
	query.Query.Conflict.UpdateColumns[0] = "id"
	manifestArtifact := &response.Output.Artifacts[0]
	changed := bytes.Replace(manifestArtifact.Bytes, []byte(`"update_columns":["email"]`), []byte(`"update_columns":["id"]`), 1)
	if bytes.Equal(changed, manifestArtifact.Bytes) {
		t.Fatal("manifest has no update columns")
	}
	manifestArtifact.Bytes = changed
	manifestArtifact.Digest = postgres.DigestSQL(string(changed))
	refreshEmitOutputDigest(t, &response)
	if err := protocol.ValidateEmitResponse(request, response); err == nil {
		t.Fatal("self-consistent conflict update metadata tamper was accepted")
	}
}

func TestEmitIncludesTypedReturningMutation(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "delete-account", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "accounts"},
		Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Int64(1)},
		Returning: []declaration.Projection{{Column: "id", Alias: "account-id"}},
		Results:   []declaration.ResultField{{ID: "id", Source: "account-id", Type: declaration.TypeInt64}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("response = %#v", response)
	}
	var query protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Path == "dataaccess/database.primary/queries/accounts.user/v1/delete-account.sql" {
			query = artifact
		}
	}
	if query.Path == "" || !bytes.Contains(query.Bytes, []byte(`DELETE FROM "accounts"."accounts" WHERE "id" = 1 RETURNING "id" AS "account-id";`)) {
		t.Fatalf("query artifact = %#v", query)
	}
	if query.Query == nil || query.Query.ID != "delete-account" || query.Query.Operation != declaration.OperationDelete || !reflect.DeepEqual(query.Query.Results, []declaration.ResultField{{ID: "id", Source: "account-id", Type: declaration.TypeInt64}}) {
		t.Fatalf("query metadata = %#v", query.Query)
	}
	var manifest struct {
		Artifacts []struct {
			Path  string                  `json:"path"`
			Query *protocol.QueryMetadata `json:"query"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(response.Output.Artifacts[0].Bytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Artifacts) != 3 || manifest.Artifacts[1].Path != query.Path || !reflect.DeepEqual(manifest.Artifacts[1].Query, query.Query) {
		t.Fatalf("manifest query metadata = %#v", manifest.Artifacts)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitIncludesCountQueryArtifactAndStableMetadata(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "count-accounts", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Projection: []declaration.Projection{{Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}},
		Results:    []declaration.ResultField{{ID: "total", Source: "total", Type: declaration.TypeInt64}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)

	response := protocol.HandleEmit(request)
	second := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded || second.Status != protocol.StatusSucceeded {
		t.Fatalf("responses = %#v, %#v", response, second)
	}
	if response.OutputDigest == "" || response.OutputDigest != second.OutputDigest || !reflect.DeepEqual(response.Output.Artifacts, second.Output.Artifacts) {
		t.Fatalf("count output is not deterministic: first=%#v second=%#v", response, second)
	}
	var query protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Query != nil && artifact.Query.ID == "count-accounts" {
			query = artifact
			break
		}
	}
	wantSQL := `SELECT COUNT("id") AS "total" FROM "accounts"."accounts";`
	if query.Path == "" || !bytes.Contains(query.Bytes, []byte(wantSQL)) || query.Query == nil ||
		!reflect.DeepEqual(query.Query.Projection, []declaration.Projection{{Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}}) ||
		!reflect.DeepEqual(query.Query.Results, []declaration.ResultField{{ID: "total", Source: "total", Type: declaration.TypeInt64}}) {
		t.Fatalf("count query artifact = %#v", query)
	}
	var manifest struct {
		Artifacts []struct {
			Path  string                  `json:"path"`
			Query *protocol.QueryMetadata `json:"query"`
		} `json:"artifacts"`
	}
	for _, artifact := range response.Output.Artifacts {
		if strings.HasSuffix(artifact.Path, "/manifest.json") {
			if err := json.Unmarshal(artifact.Bytes, &manifest); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	var manifestQuery *protocol.QueryMetadata
	for _, entry := range manifest.Artifacts {
		if entry.Path == query.Path {
			manifestQuery = entry.Query
			break
		}
	}
	if !reflect.DeepEqual(manifestQuery, query.Query) {
		t.Fatalf("manifest count metadata = %#v, query metadata = %#v", manifestQuery, query.Query)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitIncludesMinQueryArtifactAndMetadata(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "minimum-account", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Projection: []declaration.Projection{{Aggregate: declaration.AggregateMin, Column: "id", Alias: "minimum"}},
		Results:    []declaration.ResultField{{ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)

	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("response = %#v", response)
	}
	var query protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Query != nil && artifact.Query.ID == "minimum-account" {
			query = artifact
			break
		}
	}
	wantSQL := `SELECT MIN("id") AS "minimum" FROM "accounts"."accounts";`
	if query.Path == "" || !bytes.Contains(query.Bytes, []byte(wantSQL)) || query.Query == nil ||
		!reflect.DeepEqual(query.Query.Projection, []declaration.Projection{{Aggregate: declaration.AggregateMin, Column: "id", Alias: "minimum"}}) ||
		!reflect.DeepEqual(query.Query.Results, []declaration.ResultField{{ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}}) {
		t.Fatalf("min query artifact = %#v", query)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitIncludesMaxQueryArtifactAndMetadata(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "maximum-account", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Projection: []declaration.Projection{{Aggregate: declaration.AggregateMax, Column: "id", Alias: "maximum"}},
		Results:    []declaration.ResultField{{ID: "maximum", Source: "maximum", Type: declaration.TypeInt64, Nullable: true}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)

	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("response = %#v", response)
	}
	var query protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Query != nil && artifact.Query.ID == "maximum-account" {
			query = artifact
			break
		}
	}
	wantSQL := `SELECT MAX("id") AS "maximum" FROM "accounts"."accounts";`
	if query.Path == "" || !bytes.Contains(query.Bytes, []byte(wantSQL)) || query.Query == nil ||
		!reflect.DeepEqual(query.Query.Projection, []declaration.Projection{{Aggregate: declaration.AggregateMax, Column: "id", Alias: "maximum"}}) ||
		!reflect.DeepEqual(query.Query.Results, []declaration.ResultField{{ID: "maximum", Source: "maximum", Type: declaration.TypeInt64, Nullable: true}}) {
		t.Fatalf("max query artifact = %#v", query)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitIncludesGroupedQueryArtifactAndMetadata(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Tables[0].Columns = append(model.Tables[0].Columns, declaration.Column{ID: "region", Type: declaration.TypeString})
	model.Tables[0].Columns = append(model.Tables[0].Columns, declaration.Column{ID: "tier", Type: declaration.TypeString})
	model.Queries = []declaration.Query{{
		ID: "count-by-region", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		GroupBy:    []declaration.ColumnRef{{Column: "region"}, {Column: "tier"}},
		Projection: []declaration.Projection{{Column: "region"}, {Column: "tier"}, {Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}},
		Having:     declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Int64(1)},
		Results:    []declaration.ResultField{{ID: "region", Source: "region", Type: declaration.TypeString}, {ID: "tier", Source: "tier", Type: declaration.TypeString}, {ID: "total", Source: "total", Type: declaration.TypeInt64}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)

	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("response = %#v", response)
	}
	var query protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Query != nil && artifact.Query.ID == "count-by-region" {
			query = artifact
			break
		}
	}
	wantSQL := `SELECT "region", "tier", COUNT("id") AS "total" FROM "accounts"."accounts" GROUP BY "region", "tier" HAVING COUNT("id") > 1;`
	if query.Path == "" || !bytes.Contains(query.Bytes, []byte(wantSQL)) || query.Query == nil ||
		!reflect.DeepEqual(query.Query.GroupBy, []declaration.ColumnRef{{Column: "region"}, {Column: "tier"}}) ||
		query.Query.Having == nil || !reflect.DeepEqual(*query.Query.Having, declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Int64(1)}) ||
		!reflect.DeepEqual(query.Query.Projection, []declaration.Projection{{Column: "region"}, {Column: "tier"}, {Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}}) ||
		!reflect.DeepEqual(query.Query.Results, []declaration.ResultField{{ID: "region", Source: "region", Type: declaration.TypeString}, {ID: "tier", Source: "tier", Type: declaration.TypeString}, {ID: "total", Source: "total", Type: declaration.TypeInt64}}) {
		t.Fatalf("grouped query artifact = %#v", query)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
	var err error
	withoutHaving := request
	withoutHaving.Analyze.Roots = append([]analyzer.Root(nil), request.Analyze.Roots...)
	withoutHaving.Analyze.Roots[0].Model.Queries = append([]declaration.Query(nil), request.Analyze.Roots[0].Model.Queries...)
	withoutHaving.Analyze.Roots[0].Model.Queries[0].Having = declaration.Predicate{}
	withoutHaving.Analyze.Digest, err = analyzer.ResultDigest(withoutHaving.Analyze.Roots, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	withoutHaving.AnalyzeDigest = withoutHaving.Analyze.Digest
	refreshEmitDigest(t, &withoutHaving)
	plainResponse := protocol.HandleEmit(withoutHaving)
	if plainResponse.Status != protocol.StatusSucceeded {
		t.Fatalf("grouped emit without HAVING = %#v", plainResponse)
	}
	for _, artifact := range plainResponse.Output.Artifacts {
		if artifact.Query != nil && artifact.Query.ID == "count-by-region" {
			if artifact.Query.Having != nil || !bytes.Contains(artifact.Bytes, []byte(`GROUP BY "region", "tier";`)) {
				t.Fatalf("grouped artifact without HAVING = %#v", artifact)
			}
		}
	}
	if err := protocol.ValidateEmitResponse(withoutHaving, plainResponse); err != nil {
		t.Fatal(err)
	}
	for index := range response.Output.Artifacts {
		artifact := &response.Output.Artifacts[index]
		if artifact.Path == query.Path {
			artifact.Query.Having.Value.Int64 = 2
		}
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
			var path string
			if err := json.Unmarshal(entry["path"], &path); err != nil {
				t.Fatal(err)
			}
			if path != query.Path {
				continue
			}
			metadata := *query.Query
			metadata.Having = &declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Int64(2)}
			entry["query"], err = json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
		}
		manifest["artifacts"], err = json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		artifact.Bytes, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		artifact.Digest = postgres.DigestSQL(string(artifact.Bytes))
	}
	refreshEmitOutputDigest(t, &response)
	if err := protocol.ValidateEmitResponse(request, response); err == nil {
		t.Fatal("self-consistent HAVING metadata tamper was accepted")
	}
}

func TestEmitIncludesJoinedSelect(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Tables[0].Columns = append(model.Tables[0].Columns, declaration.Column{ID: "team-id", Type: declaration.TypeInt64})
	model.Tables = append(model.Tables, declaration.Table{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "region", Type: declaration.TypeString}, {ID: "amount", Type: declaration.TypeFloat64}}})
	accounts := declaration.Ref{Symbol: "accounts"}
	teams := declaration.Ref{Symbol: "teams"}
	model.Queries = []declaration.Query{{
		ID: "list-accounts", Operation: declaration.OperationSelect, Table: accounts,
		Joins: []declaration.Join{{Kind: declaration.JoinInner, Table: teams, On: declaration.JoinCondition{
			Left:  declaration.ColumnRef{Table: accounts, Column: "team-id"},
			Right: declaration.ColumnRef{Table: teams, Column: "id"},
		}}},
		Projection: []declaration.Projection{{Column: "id"}},
		Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}, {
		ID: "count-by-team-region", Operation: declaration.OperationSelect, Table: accounts,
		Joins: []declaration.Join{{Kind: declaration.JoinLeft, Table: teams, On: declaration.JoinCondition{
			Left: declaration.ColumnRef{Table: accounts, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"},
		}}},
		GroupBy:    []declaration.ColumnRef{{Table: teams, Column: "region"}},
		Projection: []declaration.Projection{{Table: teams, Column: "region", Alias: "zone"}, {Aggregate: declaration.AggregateCount, Table: teams, Column: "id", Alias: "total"}, {Aggregate: declaration.AggregateMin, Table: teams, Column: "id", Alias: "minimum"}, {Aggregate: declaration.AggregateSum, Table: teams, Column: "amount", Alias: "sum"}},
		Having:     declaration.Predicate{Kind: declaration.PredicateIsNotNull, Left: declaration.ColumnRef{Column: "zone"}},
		Ordering:   []declaration.Ordering{{Column: "zone", Direction: declaration.OrderingAscending}},
		HasLimit:   true, Limit: 2, HasOffset: true, Offset: 1,
		Results: []declaration.ResultField{{ID: "zone", Source: "zone", Type: declaration.TypeString, Nullable: true}, {ID: "total", Source: "total", Type: declaration.TypeInt64}, {ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}, {ID: "sum", Source: "sum", Type: declaration.TypeFloat64, Nullable: true}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("response = %#v", response)
	}
	var query, grouped *protocol.StagedArtifact
	for index := range response.Output.Artifacts {
		artifact := &response.Output.Artifacts[index]
		if artifact.Query != nil && artifact.Query.ID == "list-accounts" {
			query = artifact
		} else if artifact.Query != nil && artifact.Query.ID == "count-by-team-region" {
			grouped = artifact
		}
	}
	if query == nil || !bytes.Contains(query.Bytes, []byte(`INNER JOIN "accounts"."teams" AS "t1" ON "t0"."team-id" = "t1"."id"`)) || query.Query.ID != "list-accounts" || len(query.Query.Results) != 1 {
		t.Fatalf("joined query artifact = %#v", query)
	}
	if grouped == nil || !bytes.Contains(grouped.Bytes, []byte(`COUNT("t1"."id") AS "total", MIN("t1"."id") AS "minimum", SUM("t1"."amount") AS "sum"`)) || !bytes.Contains(grouped.Bytes, []byte(`GROUP BY "t1"."region" HAVING "t1"."region" IS NOT NULL ORDER BY "t1"."region" ASC LIMIT 2 OFFSET 1;`)) ||
		!reflect.DeepEqual(grouped.Query.GroupBy, []declaration.ColumnRef{{Table: teams, Column: "region"}}) || grouped.Query.Having == nil ||
		!reflect.DeepEqual(grouped.Query.Results, []declaration.ResultField{{ID: "zone", Source: "zone", Type: declaration.TypeString, Nullable: true}, {ID: "total", Source: "total", Type: declaration.TypeInt64}, {ID: "minimum", Source: "minimum", Type: declaration.TypeInt64, Nullable: true}, {ID: "sum", Source: "sum", Type: declaration.TypeFloat64, Nullable: true}}) {
		t.Fatalf("grouped joined query artifact = %#v", grouped)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
	tampered := response
	tampered.Output.Artifacts = append([]protocol.StagedArtifact(nil), response.Output.Artifacts...)
	for index := range tampered.Output.Artifacts {
		if tampered.Output.Artifacts[index].Query == nil || tampered.Output.Artifacts[index].Query.ID != "count-by-team-region" {
			continue
		}
		metadata := *tampered.Output.Artifacts[index].Query
		metadata.Projection = append([]declaration.Projection(nil), metadata.Projection...)
		metadata.Projection[3].Aggregate = declaration.AggregateMax
		tampered.Output.Artifacts[index].Query = &metadata
	}
	if err := protocol.ValidateEmitResponse(request, tampered); err == nil {
		t.Fatal("SUM metadata tamper was accepted")
	}
}

func TestEmitIncludesExecutableAtomicPlanArtifact(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Projection: []declaration.Projection{{Column: "id"}},
		Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}}
	model.Plans = []declaration.Plan{{ID: "read-plan", Statements: []declaration.PlanStatement{{ID: "read", Query: declaration.Ref{Symbol: "read"}, Cardinality: declaration.CardinalityMany}}}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Plans: model.Plans, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Artifacts) != 5 {
		t.Fatalf("response = %#v", response)
	}
	var plan protocol.StagedArtifact
	for _, artifact := range response.Output.Artifacts {
		if artifact.Plan != nil {
			plan = artifact
		}
	}
	if plan.Path != "dataaccess/database.primary/plans/accounts.user/v1/read-plan.json" || plan.Plan == nil || plan.Plan.ID != "read-plan" || len(plan.Plan.Statements) != 1 || plan.Plan.Statements[0].Query != "read" || !bytes.Contains(plan.Bytes, []byte(`"cardinality":"many"`)) {
		t.Fatalf("plan artifact = %#v", plan)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
	tampered := cloneEmitResponse(response)
	for index := range tampered.Output.Artifacts {
		artifact := &tampered.Output.Artifacts[index]
		if artifact.Plan == nil {
			continue
		}
		metadata := *artifact.Plan
		metadata.Statements = append([]protocol.PlanStatementMetadata(nil), metadata.Statements...)
		metadata.Statements[0].Cardinality = "exactly_one"
		encoded, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		artifact.Plan = &metadata
		artifact.Bytes = encoded
		artifact.Digest = postgres.DigestSQL(string(encoded))
	}
	refreshEmitOutputDigest(t, &tampered)
	if err := protocol.ValidateEmitResponse(request, tampered); err == nil {
		t.Fatal("self-consistent plan metadata tamper was accepted")
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

func TestEmitRejectsInvalidMemberIDBeforePathConstruction(t *testing.T) {
	request := validEmitRequest(t)
	request.Analyze.Roots[0].MemberID = "accounts.user/v1\ninvalid"
	request.Assignments[0].MemberID = request.Analyze.Roots[0].MemberID
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 || response.Diagnostics[0].Code != analyzer.CodeEmitInput {
		t.Fatalf("invalid member ID response = %#v", response)
	}
}

func TestEmitAcceptsMemberIDBeyondLocalSymbolLimit(t *testing.T) {
	request := validEmitRequest(t)
	id := strings.Repeat("a", 124) + ".b/v1"
	request.Analyze.Roots[0].MemberID = id
	request.Assignments[0].MemberID = id
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("long member ID emit = %#v", response)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatalf("long member ID response: %v", err)
	}
}

func TestValidateEmitResponseRejectsSelfConsistentQueryMetadataTamper(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "read-account", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Projection: []declaration.Projection{{Column: "id"}},
		Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded {
		t.Fatalf("response = %#v", response)
	}
	for index := range response.Output.Artifacts {
		if response.Output.Artifacts[index].Query != nil {
			response.Output.Artifacts[index].Query.Results[0].Type = declaration.TypeString
		}
	}
	manifest := &response.Output.Artifacts[0]
	changed := bytes.Replace(manifest.Bytes, []byte(`"type":"int64"`), []byte(`"type":"string"`), 1)
	if bytes.Equal(changed, manifest.Bytes) {
		t.Fatal("manifest has no query result type")
	}
	manifest.Bytes = changed
	manifest.Digest = postgres.DigestSQL(string(changed))
	refreshEmitOutputDigest(t, &response)
	if err := protocol.ValidateEmitResponse(request, response); err == nil {
		t.Fatal("self-consistent query metadata tamper was accepted")
	}
}

func TestValidateEmitResponseRejectsSelfConsistentSQLTamper(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "read-account", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
		Projection: []declaration.Projection{{Column: "id"}},
		Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
		Ordering:   []declaration.Ordering{{Column: "id", Direction: declaration.OrderingAscending}},
		HasLimit:   true, Limit: 2, HasOffset: true, Offset: 1,
	}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Migrations: model.Migrations})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	base := protocol.HandleEmit(request)
	if base.Status != protocol.StatusSucceeded {
		t.Fatalf("base response = %#v", base)
	}
	for _, test := range []struct{ name, suffix string }{
		{"schema", "/schema/schema.sql"},
		{"query", "/queries/accounts.user/v1/read-account.sql"},
		{"migration", "/migrations/accounts.user/v1/create-accounts.sql"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := cloneEmitResponse(base)
			var changed *protocol.StagedArtifact
			for index := range response.Output.Artifacts {
				artifact := &response.Output.Artifacts[index]
				if strings.HasSuffix(artifact.Path, test.suffix) {
					if test.name == "query" {
						if !bytes.Contains(artifact.Bytes, []byte(" LIMIT 2 OFFSET 1;")) {
							t.Fatal("query SQL has no accepted pagination")
						}
						artifact.Bytes = bytes.Replace(artifact.Bytes, []byte(" LIMIT 2 OFFSET 1;"), []byte(" LIMIT 200 OFFSET 0;"), 1)
					} else {
						artifact.Bytes = append(append([]byte(nil), artifact.Bytes...), []byte("-- altered SQL\n")...)
					}
					artifact.Digest = postgres.DigestSQL(string(artifact.Bytes))
					changed = artifact
					break
				}
			}
			if changed == nil {
				t.Fatalf("no artifact ending in %s", test.suffix)
			}
			for index := range response.Output.Artifacts {
				manifest := &response.Output.Artifacts[index]
				if !strings.HasSuffix(manifest.Path, "/manifest.json") {
					continue
				}
				var document map[string]json.RawMessage
				if err := json.Unmarshal(manifest.Bytes, &document); err != nil {
					t.Fatal(err)
				}
				var entries []map[string]json.RawMessage
				if err := json.Unmarshal(document["artifacts"], &entries); err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					var path string
					if err := json.Unmarshal(entry["path"], &path); err != nil {
						t.Fatal(err)
					}
					if path == changed.Path {
						var err error
						entry["digest"], err = json.Marshal(changed.Digest)
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				var err error
				document["artifacts"], err = json.Marshal(entries)
				if err != nil {
					t.Fatal(err)
				}
				manifest.Bytes, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				manifest.Digest = postgres.DigestSQL(string(manifest.Bytes))
			}
			refreshEmitOutputDigest(t, &response)
			if err := protocol.ValidateEmitResponse(request, response); err == nil {
				t.Fatal("self-consistent SQL tamper was accepted")
			}
		})
	}
}

func TestValidateEmitResponseRejectsSelfConsistentMigrationDefinitionTamper(t *testing.T) {
	request := validEmitRequest(t)
	base := protocol.HandleEmit(request)
	if base.Status != protocol.StatusSucceeded {
		t.Fatalf("base = %#v", base)
	}
	tests := []struct {
		name string
		edit func(*migration.Definition)
	}{
		{"version", func(definition *migration.Definition) { definition.Version++ }},
		{"predecessors", func(definition *migration.Definition) {
			definition.Predecessors = []migration.Identity{{Member: "accounts.user/v1", Name: "previous"}}
		}},
		{"logical digest", func(definition *migration.Definition) { definition.LogicalDigest = "sha256:" + strings.Repeat("9", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := cloneEmitResponse(base)
			var migrationPath string
			var definition migration.Definition
			for index := range response.Output.Artifacts {
				if response.Output.Artifacts[index].Migration == nil {
					continue
				}
				definition = *response.Output.Artifacts[index].Migration
				test.edit(&definition)
				response.Output.Artifacts[index].Migration = &definition
				migrationPath = response.Output.Artifacts[index].Path
				break
			}
			if migrationPath == "" {
				t.Fatal("base response has no migration artifact")
			}
			setManifestMigration(t, &response, migrationPath, &definition)
			refreshEmitOutputDigest(t, &response)
			if err := protocol.ValidateEmitResponse(request, response); err == nil {
				t.Fatal("self-consistent migration definition tamper was accepted")
			}
		})
	}
}

func TestValidateEmitResponseRejectsMigrationMetadataOnNonMigrationArtifact(t *testing.T) {
	request := validEmitRequest(t)
	base := protocol.HandleEmit(request)
	if base.Status != protocol.StatusSucceeded {
		t.Fatalf("base = %#v", base)
	}
	response := cloneEmitResponse(base)
	var definition migration.Definition
	var schemaPath string
	for index := range response.Output.Artifacts {
		if strings.HasSuffix(response.Output.Artifacts[index].Path, "/schema/schema.sql") {
			definition = *base.Output.Artifacts[1].Migration
			response.Output.Artifacts[index].Migration = &definition
			schemaPath = response.Output.Artifacts[index].Path
			break
		}
	}
	if schemaPath == "" {
		t.Fatal("base response has no schema artifact")
	}
	setManifestMigration(t, &response, schemaPath, &definition)
	refreshEmitOutputDigest(t, &response)
	if err := protocol.ValidateEmitResponse(request, response); err == nil {
		t.Fatal("non-migration artifact with self-consistent migration metadata was accepted")
	}
}

func TestEmitRejectsUnsafeRootAndUnsupportedBackend(t *testing.T) {
	tests := []struct {
		name       string
		assignment protocol.EmitAssignment
		code       analyzer.DiagnosticCode
	}{
		{"traversal", protocol.EmitAssignment{AllowedRoot: "dataaccess/database.primary/../../outside"}, analyzer.CodeEmitPath},
		{"absolute", protocol.EmitAssignment{AllowedRoot: "C:/outside"}, analyzer.CodeEmitPath},
		{"unselected root", protocol.EmitAssignment{AllowedRoot: "dataaccess/database.other"}, analyzer.CodeEmitPath},
		{"superseded Core-owned root", protocol.EmitAssignment{AllowedRoot: "generated/data/database.primary"}, analyzer.CodeEmitPath},
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
	second := analyzer.Root{MemberID: "audit.events/v1", Model: normalized(t, declaration.Member[struct{}]{Namespace: "audit", Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "pk-events", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}}}}), MigrationOnly: true, AccessPackage: "github.com/plystra/data/declaration", AccessType: "NoAccess"}
	request.Analyze.Roots = append(request.Analyze.Roots, second)
	request.Analyze.Roots[0].Model.Imports = []string{second.MemberID}
	request.Analyze.Roots[0].Model, _ = (declaration.Member[struct{}]{Namespace: request.Analyze.Roots[0].Model.Namespace, Imports: request.Analyze.Roots[0].Model.Imports, Tables: request.Analyze.Roots[0].Model.Tables, Migrations: request.Analyze.Roots[0].Model.Migrations}).Normalize()
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	if response := protocol.HandleEmit(request); response.Status != protocol.StatusRejected || response.Diagnostics[0].Code != analyzer.CodeEmitBackend {
		t.Fatalf("inactive import response = %#v", response)
	}
	request.Assignments = append(request.Assignments, protocol.EmitAssignment{MemberID: second.MemberID, Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "dataaccess/database.primary"})
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusSucceeded || len(response.Output.Artifacts) != 3 || !reflect.DeepEqual(response.Output.Artifacts[2].OwningMembers, []string{"accounts.user/v1", "audit.events/v1"}) {
		t.Fatalf("shared Resource response = %#v", response)
	}
	request.Assignments[1].Resource = "database.audit"
	request.Assignments[1].AllowedRoot = "dataaccess/database.audit"
	refreshEmitDigest(t, &request)
	if response := protocol.HandleEmit(request); response.Status != protocol.StatusRejected || response.Diagnostics[0].Code != analyzer.CodeEmitBackend {
		t.Fatalf("cross-Resource import response = %#v", response)
	}
}

func TestEmitOutputIsDeterministicAcrossAssignmentOrder(t *testing.T) {
	request := validEmitRequest(t)
	second := analyzer.Root{MemberID: "audit.events/v1", Model: normalized(t, declaration.Member[struct{}]{Namespace: "audit", Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}}}}), MigrationOnly: true, AccessPackage: "github.com/plystra/data/declaration", AccessType: "NoAccess"}
	request.Analyze.Roots = append(request.Analyze.Roots, second)
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	request.Assignments = append(request.Assignments, protocol.EmitAssignment{MemberID: second.MemberID, Resource: "database.audit", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "dataaccess/database.audit"})
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
			r.Output.Artifacts[0].Path = "dataaccess/database.primary/../outside"
		}},
		{"superseded Core-owned path", func(r *protocol.EmitResponse) {
			r.Output.Artifacts[0].Path = "generated/data/database.primary/manifest.json"
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

func TestValidateEmitResponseRejectsDiagnosticBounds(t *testing.T) {
	base := validEmitRequest(t)
	tests := []struct {
		name   string
		limits analyzer.Limits
		items  []analyzer.Diagnostic
	}{
		{
			name:   "count",
			limits: analyzer.Limits{MaxDiagnostics: 1},
			items: []analyzer.Diagnostic{
				{Code: analyzer.CodeEmitInput, Message: "one", Action: "correct"},
				{Code: analyzer.CodeEmitInput, Message: "two", Action: "correct"},
			},
		},
		{
			name:   "bytes",
			limits: analyzer.Limits{MaxDiagnosticBytes: 1},
			items:  []analyzer.Diagnostic{{Code: analyzer.CodeEmitInput, Message: "too large", Action: "correct"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Limits = test.limits
			refreshEmitDigest(t, &request)
			response := protocol.EmitResponse{
				Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: request.RequestID,
				InputDigest: request.InputDigest, Status: protocol.StatusRejected, Diagnostics: test.items,
			}
			if err := protocol.ValidateEmitResponse(request, response); err == nil {
				t.Fatal("out-of-bounds diagnostics accepted")
			}
		})
	}
}

func validEmitRequest(t *testing.T) protocol.EmitRequest {
	t.Helper()
	table := declaration.Table{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "pk-accounts", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}}
	model := normalized(t, declaration.Member[struct{}]{Namespace: "accounts", Tables: []declaration.Table{table}, Migrations: []declaration.Migration{{ID: "create-accounts", Version: 1, Snapshot: []declaration.MigrationSchema{{Namespace: "accounts", Tables: []declaration.Table{table}}}, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "accounts"}}}}}})
	roots := []analyzer.Root{{MemberID: "accounts.user/v1", Model: model, MigrationOnly: true, AccessPackage: "github.com/plystra/data/declaration", AccessType: "NoAccess"}}
	resultDigest, err := analyzer.ResultDigest(roots, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "emit-1",
		Compiler: validRequest(t).Compiler, Build: protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"},
		Analyze: protocol.AnalyzeOutput{Roots: roots, Digest: resultDigest, Valid: true}, AnalyzeDigest: resultDigest,
		FrozenModelDigest: "sha256:" + strings.Repeat("4", 64),
		Assignments:       []protocol.EmitAssignment{{MemberID: "accounts.user/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "dataaccess/database.primary"}},
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

func refreshEmitOutputDigest(t *testing.T, response *protocol.EmitResponse) {
	t.Helper()
	payload, err := json.Marshal(response.Output.Artifacts)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append([]byte("plystra.data.emit-output/v1\x00"), payload...))
	response.OutputDigest = "sha256:" + hex.EncodeToString(sum[:])
}

func cloneEmitResponse(base protocol.EmitResponse) protocol.EmitResponse {
	response := base
	response.Output.Artifacts = append([]protocol.StagedArtifact(nil), base.Output.Artifacts...)
	for index := range response.Output.Artifacts {
		if definition := response.Output.Artifacts[index].Migration; definition != nil {
			copy := *definition
			copy.Predecessors = append([]migration.Identity(nil), definition.Predecessors...)
			response.Output.Artifacts[index].Migration = &copy
		}
	}
	return response
}

func setManifestMigration(t *testing.T, response *protocol.EmitResponse, path string, definition *migration.Definition) {
	t.Helper()
	var manifestIndex int
	for index := range response.Output.Artifacts {
		if strings.HasSuffix(response.Output.Artifacts[index].Path, "/manifest.json") {
			manifestIndex = index
			break
		}
	}
	manifestArtifact := &response.Output.Artifacts[manifestIndex]
	var document map[string]json.RawMessage
	if err := json.Unmarshal(manifestArtifact.Bytes, &document); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(document["artifacts"], &entries); err != nil {
		t.Fatal(err)
	}
	definitionJSON, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		var entryPath string
		if err := json.Unmarshal(entry["path"], &entryPath); err != nil {
			t.Fatal(err)
		}
		if entryPath == path {
			entry["migration"] = definitionJSON
			found = true
		}
	}
	if !found {
		t.Fatalf("manifest has no entry for %s", path)
	}
	document["artifacts"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	manifestArtifact.Bytes, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	manifestArtifact.Digest = postgres.DigestSQL(string(manifestArtifact.Bytes))
}
