package analyzer_test

import (
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
)

func TestAnalyzeCallablePlanParametersBindingsAndOutputs(t *testing.T) {
	result := analyzeBoundPlan(t, boundPlanSource, "Digest []byte; Name string; AccountID int64; CredentialID int64", "AccountID int64; CredentialID int64; Note *string")
	if !result.Valid || len(result.Roots) != 1 || len(result.Diagnostics) != 0 {
		t.Fatalf("bound plan = %#v", result)
	}
	root := result.Roots[0]
	if len(root.PlanMethods) != 1 || root.PlanMethods[0].Name != "Register" || root.PlanMethods[0].RequestType != "Request" || root.PlanMethods[0].ResultType != "Result" || len(root.QueryMethods) != 0 {
		t.Fatalf("plan method identities = %#v", root.PlanMethods)
	}
	plan := root.Model.Plans[0]
	if plan.Parameters[0].ID != "account-id" || plan.Parameters[0].GoField != "AccountID" || plan.Statements[1].Bindings[0].FromStatement != "account" || plan.Statements[1].Bindings[0].FromResult != "id" || plan.Statements[2].ID != "finish" || plan.Outputs[1].Statement != "credential" {
		t.Fatalf("decoded plan = %#v", plan)
	}
	changed := strings.Replace(boundPlanSource, `FromStatement: "account", FromResult: "id"`, `FromParameter: "account-id"`, 1)
	other := analyzeBoundPlan(t, changed, "Digest []byte; Name string; AccountID int64; CredentialID int64", "AccountID int64; CredentialID int64; Note *string")
	if !other.Valid || other.Digest == result.Digest {
		t.Fatal("binding source did not bind accepted analysis digest")
	}
}

func TestAnalyzeCallablePlanMapsSameResultIDByStatement(t *testing.T) {
	body := strings.Replace(boundPlanSource,
		`Returning: []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}}`,
		`Returning: []declaration.Projection{{Column: "digest"}}, Results: []declaration.ResultField{{ID: "id", Source: "digest", Type: "bytes"}}`, 1)
	result := analyzeBoundPlan(t, body, "Digest []byte; Name string; AccountID int64; CredentialID int64", "AccountID int64; CredentialID []byte; Note *string")
	if !result.Valid {
		t.Fatalf("per-statement result mapping = %#v", result.Diagnostics)
	}
	wrong := analyzeBoundPlan(t, body, "Digest []byte; Name string; AccountID int64; CredentialID int64", "AccountID int64; CredentialID int64; Note *string")
	if wrong.Valid || !hasCode(wrong, analyzer.CodeAccessContractMismatch) {
		t.Fatalf("wrong statement result type accepted: %#v", wrong)
	}
}

func TestAnalyzeCallablePlanEmptyResultAndAllInputScalars(t *testing.T) {
	for _, scalar := range []struct{ kind, goType string }{{"bool", "bool"}, {"bytes", "[]byte"}, {"float64", "float64"}, {"int64", "int64"}, {"string", "string"}} {
		t.Run(scalar.kind, func(t *testing.T) {
			body := strings.ReplaceAll(boundPlanSource, `"bytes"`, `"`+scalar.kind+`"`)
			body = strings.Replace(body, boundPlanOutputs, "", 1)
			result := analyzeBoundPlan(t, body, "Digest "+scalar.goType+"; Name string; AccountID int64; CredentialID int64", "")
			if !result.Valid || len(result.Roots) != 1 || result.Roots[0].PlanMethods[0].ResultType != "Result" {
				t.Fatalf("empty result plan = %#v", result)
			}
		})
	}
	const noOutputSelect = `package model
import ("github.com/plystra/data/database"; "github.com/plystra/data/declaration")
//plystra:data accounts.writer/v1
var Accounts = declaration.Member[database.Resource]{Namespace: "accounts",
	Tables: []declaration.Table{{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}},
	Queries: []declaration.Query{{ID: "read", Operation: "select", Table: declaration.Ref{Symbol: "accounts"}, Projection: []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}}}},
	Plans: []declaration.Plan{{ID: "register", Method: "Register", Statements: []declaration.PlanStatement{{ID: "read", Query: declaration.Ref{Symbol: "read"}, Cardinality: "CARDINALITY"}}}},
}`
	for _, cardinality := range []string{"exactly_one", "zero_or_one", "many"} {
		result := analyzeBoundPlan(t, strings.Replace(noOutputSelect, "CARDINALITY", cardinality, 1), "", "")
		if !result.Valid {
			t.Fatalf("unretained %s SELECT = %#v", cardinality, result.Diagnostics)
		}
	}
}

func TestAnalyzeCallablePlanRejectsInvalidContracts(t *testing.T) {
	const request = "Digest []byte; Name string; AccountID int64; CredentialID int64"
	const output = "AccountID int64; CredentialID int64; Note *string"
	tests := []struct {
		name            string
		from, to        string
		request, result string
		code            analyzer.DiagnosticCode
	}{
		{name: "request extra field", request: request + "; Extra bool", result: output, code: analyzer.CodeAccessContractMismatch},
		{name: "request missing field", request: "Digest []byte; Name string; AccountID int64", result: output, code: analyzer.CodeAccessContractMismatch},
		{name: "request wrong type", request: "Digest string; Name string; AccountID int64; CredentialID int64", result: output, code: analyzer.CodeAccessContractMismatch},
		{name: "result extra field", request: request, result: output + "; Extra bool", code: analyzer.CodeAccessContractMismatch},
		{name: "result missing field", request: request, result: "AccountID int64; CredentialID int64", code: analyzer.CodeAccessContractMismatch},
		{name: "result nullability", request: request, result: "AccountID int64; CredentialID int64; Note string", code: analyzer.CodeAccessContractMismatch},
		{name: "unknown input", from: `FromParameter: "digest"`, to: `FromParameter: "missing"`, request: request, result: output, code: analyzer.CodeUnresolvedReference},
		{name: "mixed input and result", from: `FromParameter: "digest"`, to: `FromParameter: "digest", FromResult: "id"`, request: request, result: output, code: analyzer.CodeInvalidRootShape},
		{name: "self reference", from: `FromStatement: "account", FromResult: "id"`, to: `FromStatement: "credential", FromResult: "id"`, request: request, result: output, code: analyzer.CodeUnresolvedReference},
		{name: "forward reference", from: `FromStatement: "account", FromResult: "id"`, to: `FromStatement: "finish", FromResult: "id"`, request: request, result: output, code: analyzer.CodeUnresolvedReference},
		{name: "missing result", from: `FromStatement: "account", FromResult: "id"`, to: `FromStatement: "account"`, request: request, result: output, code: analyzer.CodeInvalidRootShape},
		{name: "missing binding", from: `{Parameter: "name", FromParameter: "name"},`, to: "", request: request, result: output, code: analyzer.CodeInvalidRootShape},
		{name: "nullable source", from: `{ID: "id", Source: "id", Type: "int64"}`, to: `{ID: "id", Source: "id", Type: "int64", Nullable: true}`, request: request, result: output, code: analyzer.CodeUnresolvedReference},
		{name: "many source", from: `Cardinality: "exactly_one"`, to: `Cardinality: "many"`, request: request, result: output, code: analyzer.CodeInvalidRootShape},
		{name: "returning missing affected", from: `Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1},`, to: "", request: request, result: output, code: analyzer.CodeInvalidRootShape},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := boundPlanSource
			if test.from != "" {
				body = strings.Replace(body, test.from, test.to, 1)
				if body == boundPlanSource {
					t.Fatal("invalid fixture replacement")
				}
			}
			result := analyzeBoundPlan(t, body, test.request, test.result)
			if result.Valid || len(result.Roots) != 0 || !hasCode(result, test.code) {
				t.Fatalf("invalid plan = %#v", result)
			}
		})
	}
}

func analyzeBoundPlan(t *testing.T, body, requestFields, resultFields string) analyzer.Result {
	t.Helper()
	snapshot := sourceSnapshot(body, analyzer.ResourceContract{ImportPath: "github.com/plystra/data/database", TypeName: "Resource", ID: resourceID})
	snapshot.Packages[1].Files[0].Content = []byte("package database\nimport \"context\"\ntype Request struct{" + requestFields + "}\ntype Result struct{" + resultFields + "}\ntype Resource interface { Register(context.Context, Request) (Result, error) }\n")
	result, err := analyzer.Analyze(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

const boundPlanOutputs = `Outputs: []declaration.PlanOutput{{Statement: "account", Result: "id", GoField: "AccountID"}, {Statement: "credential", Result: "id", GoField: "CredentialID"}, {Statement: "account", Result: "note", GoField: "Note"}},`

const boundPlanSource = `package model
import ("github.com/plystra/data/database"; "github.com/plystra/data/declaration")
//plystra:data accounts.writer/v1
var Accounts = declaration.Member[database.Resource]{Namespace: "accounts",
	Tables: []declaration.Table{
		{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "name", Type: "string"}, {ID: "note", Type: "string", Nullable: true}}},
		{ID: "credentials", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "account", Type: "int64"}, {ID: "digest", Type: "bytes"}}},
	},
	Queries: []declaration.Query{
		{ID: "create-account", Operation: "insert", Table: declaration.Ref{Symbol: "accounts"}, Parameters: []declaration.Parameter{{ID: "id", Type: "int64"}, {ID: "name", Type: "string"}},
			Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: "int64", Set: true, Parameter: "id"}}, {Column: "name", Value: declaration.Value{Kind: "string", Set: true, Parameter: "name"}}},
			Returning: []declaration.Projection{{Column: "id"}, {Column: "note"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}, {ID: "note", Source: "note", Type: "string", Nullable: true}}},
		{ID: "create-credential", Operation: "insert", Table: declaration.Ref{Symbol: "credentials"}, Parameters: []declaration.Parameter{{ID: "account", Type: "int64"}, {ID: "id", Type: "int64"}, {ID: "digest", Type: "bytes"}},
			Assignments: []declaration.Assignment{{Column: "account", Value: declaration.Value{Kind: "int64", Set: true, Parameter: "account"}}, {Column: "id", Value: declaration.Value{Kind: "int64", Set: true, Parameter: "id"}}, {Column: "digest", Value: declaration.Value{Kind: "bytes", Set: true, Parameter: "digest"}}},
			Returning: []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}}},
		{ID: "finish-account", Operation: "update", Table: declaration.Ref{Symbol: "accounts"}, Parameters: []declaration.Parameter{{ID: "id", Type: "int64"}},
			Assignments: []declaration.Assignment{{Column: "name", Value: declaration.Value{Kind: "string", Set: true, String: "registered"}}}, Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Parameter: "id"}}},
	},
	Plans: []declaration.Plan{{ID: "register", Method: "Register",
		Parameters: []declaration.Parameter{{ID: "digest", Type: "bytes", GoField: "Digest"}, {ID: "name", Type: "string", GoField: "Name"}, {ID: "account-id", Type: "int64", GoField: "AccountID"}, {ID: "credential-id", Type: "int64", GoField: "CredentialID"}},
		Statements: []declaration.PlanStatement{
			{ID: "account", Query: declaration.Ref{Symbol: "create-account"}, Cardinality: "exactly_one", Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "name", FromParameter: "name"}, {Parameter: "id", FromParameter: "account-id"}}},
			{ID: "credential", Query: declaration.Ref{Symbol: "create-credential"}, Cardinality: "exactly_one", Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "account", FromStatement: "account", FromResult: "id"}, {Parameter: "id", FromParameter: "credential-id"}, {Parameter: "digest", FromParameter: "digest"}}},
			{ID: "finish", Query: declaration.Ref{Symbol: "finish-account"}, Cardinality: "none", Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromStatement: "account", FromResult: "id"}}},
		},
		` + boundPlanOutputs + `
	}},
}`
