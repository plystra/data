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
	if response.Output.Artifacts[2].Path != "generated/data/database.primary/schema/schema.sql" || !reflect.DeepEqual(response.Output.Artifacts[2].OwningMembers, []string{"accounts.user/v1"}) {
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
		if artifact.Path == "generated/data/database.primary/queries/accounts.user/v1/list-accounts.sql" {
			query = artifact
		}
	}
	if query.Path == "" || query.Migration != nil || !bytes.Contains(query.Bytes, []byte(`SELECT "id" FROM "accounts"."accounts";`)) || !reflect.DeepEqual(query.OwningMembers, []string{"accounts.user/v1"}) {
		t.Fatalf("query artifact = %#v", query)
	}
	if query.Query == nil || query.Query.ID != "list-accounts" || query.Query.Operation != declaration.OperationSelect || len(query.Query.Parameters) != 0 || len(query.Query.Results) != 0 {
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

func TestEmitIncludesTypedMutationQueryArtifact(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "update-account", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"},
		Parameters:  []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
		Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}}},
		Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
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
		if artifact.Path == "generated/data/database.primary/queries/accounts.user/v1/update-account.sql" {
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

func TestEmitIncludesTypedReturningMutation(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{
		ID: "delete-account", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "accounts"},
		Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: "id", Value: declaration.Int64(1)},
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
		if artifact.Path == "generated/data/database.primary/queries/accounts.user/v1/delete-account.sql" {
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

func TestEmitIncludesJoinedSelect(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Tables[0].Columns = append(model.Tables[0].Columns, declaration.Column{ID: "team-id", Type: declaration.TypeInt64})
	model.Tables = append(model.Tables, declaration.Table{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}}})
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
		artifact := &response.Output.Artifacts[index]
		if artifact.Query != nil {
			query = artifact
		}
	}
	if query == nil || !bytes.Contains(query.Bytes, []byte(`INNER JOIN "accounts"."teams" AS "t1" ON "t0"."team-id" = "t1"."id"`)) || query.Query.ID != "list-accounts" || len(query.Query.Results) != 1 {
		t.Fatalf("joined query artifact = %#v", query)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		t.Fatal(err)
	}
}

func TestEmitRejectsAtomicPlansUntilExecutableArtifactsExist(t *testing.T) {
	request := validEmitRequest(t)
	model := request.Analyze.Roots[0].Model
	model.Queries = []declaration.Query{{ID: "read", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"}}}
	model.Plans = []declaration.Plan{{ID: "read-plan", Statements: []declaration.PlanStatement{{ID: "read", Query: declaration.Ref{Symbol: "read"}, Cardinality: declaration.CardinalityMany}}}}
	request.Analyze.Roots[0].Model = normalized(t, declaration.Member[struct{}]{
		Namespace: model.Namespace, Tables: model.Tables, Queries: model.Queries, Plans: model.Plans, Migrations: model.Migrations,
	})
	request.Analyze.Digest, _ = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	request.AnalyzeDigest = request.Analyze.Digest
	refreshEmitDigest(t, &request)
	response := protocol.HandleEmit(request)
	if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 || len(response.Diagnostics) != 1 || response.Diagnostics[0].Code != analyzer.CodeEmitBackend {
		t.Fatalf("response = %#v", response)
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
