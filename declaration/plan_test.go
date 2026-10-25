package declaration_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/plystra/data/declaration"
)

func TestNormalizePlanParametersBindingsAndCrossStatementOutputs(t *testing.T) {
	member := boundPlanMember()
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	plan := model.Plans[0]
	if plan.Parameters[0].ID != "account-id" || plan.Statements[0].Bindings[0].Parameter != "id" || plan.Statements[1].Bindings[0].Parameter != "account" {
		t.Fatalf("plan metadata was not normalized: %#v", plan)
	}
	if plan.Statements[0].ID != "account" || plan.Statements[1].ID != "credential" || plan.Outputs[0].GoField != "AccountID" || plan.Outputs[1].GoField != "CredentialID" {
		t.Fatal("statement or output order changed")
	}
	digest, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	reordered := boundPlanMember()
	reordered.Plans[0].Parameters[0], reordered.Plans[0].Parameters[3] = reordered.Plans[0].Parameters[3], reordered.Plans[0].Parameters[0]
	reordered.Plans[0].Statements[1].Bindings[0], reordered.Plans[0].Statements[1].Bindings[2] = reordered.Plans[0].Statements[1].Bindings[2], reordered.Plans[0].Statements[1].Bindings[0]
	if other, err := reordered.Digest(); err != nil || other != digest {
		t.Fatalf("equivalent binding order changed digest: %s, %v", other, err)
	}
	plan.Parameters[0].GoField = "Changed"
	plan.Statements[0].Bindings[0].FromParameter = "changed"
	if !reflect.DeepEqual(member, boundPlanMember()) {
		t.Fatal("normalized plan aliases authored parameters or bindings")
	}
	changed := boundPlanMember()
	changed.Plans[0].Statements[1].Bindings[0] = declaration.PlanBinding{Parameter: "account", FromParameter: "account-id"}
	if other, err := changed.Digest(); err != nil || other == digest {
		t.Fatalf("binding source did not bind digest: %s, %v", other, err)
	}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var decoded declaration.Model
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(decoded.Plans, model.Plans) {
		t.Fatalf("plan JSON round trip = %v", err)
	}
}

func TestNormalizePlanMetadataCountsTowardNodeLimit(t *testing.T) {
	for _, field := range []string{"parameters", "bindings"} {
		t.Run(field, func(t *testing.T) {
			member := boundPlanMember()
			if field == "parameters" {
				member.Plans[0].Parameters = make([]declaration.Parameter, declaration.MaximumNodes)
			} else {
				member.Plans[0].Statements[0].Bindings = make([]declaration.PlanBinding, declaration.MaximumNodes)
			}
			if _, err := member.Normalize(); !errors.Is(err, declaration.ErrLimit) {
				t.Fatalf("plan %s bypassed node bounds: %v", field, err)
			}
		})
	}
}

func TestNormalizePlanRejectsInvalidBindings(t *testing.T) {
	tests := []struct {
		name string
		edit func(*declaration.Member[access])
		want error
	}{
		{"missing bindings", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Bindings = nil }, declaration.ErrInvalidSchema},
		{"duplicate binding", func(m *declaration.Member[access]) {
			m.Plans[0].Statements[0].Bindings = append(m.Plans[0].Statements[0].Bindings, m.Plans[0].Statements[0].Bindings[0])
		}, declaration.ErrDuplicate},
		{"unknown target", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Bindings[0].Parameter = "missing" }, declaration.ErrReference},
		{"absent source", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Bindings[0].FromParameter = "" }, declaration.ErrInvalidSchema},
		{"mixed sources", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Bindings[0].FromStatement = "account" }, declaration.ErrInvalidSchema},
		{"missing result field", func(m *declaration.Member[access]) { m.Plans[0].Statements[1].Bindings[0].FromResult = "" }, declaration.ErrInvalidSchema},
		{"missing result statement", func(m *declaration.Member[access]) { m.Plans[0].Statements[1].Bindings[0].FromStatement = "" }, declaration.ErrInvalidSchema},
		{"unknown input", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Bindings[0].FromParameter = "missing" }, declaration.ErrReference},
		{"input type", func(m *declaration.Member[access]) { m.Plans[0].Parameters[0].Type = declaration.TypeInt64 }, declaration.ErrReference},
		{"unknown statement", func(m *declaration.Member[access]) { m.Plans[0].Statements[1].Bindings[0].FromStatement = "missing" }, declaration.ErrReference},
		{"self reference", func(m *declaration.Member[access]) { m.Plans[0].Statements[1].Bindings[0].FromStatement = "credential" }, declaration.ErrReference},
		{"forward reference", func(m *declaration.Member[access]) { m.Plans[0].Statements[1].Bindings[0].FromStatement = "finish" }, declaration.ErrReference},
		{"unknown result", func(m *declaration.Member[access]) { m.Plans[0].Statements[1].Bindings[0].FromResult = "missing" }, declaration.ErrReference},
		{"nullable result", func(m *declaration.Member[access]) { m.Queries[0].Results[0].Nullable = true }, declaration.ErrReference},
		{"result type", func(m *declaration.Member[access]) { m.Queries[0].Results[0].Type = declaration.TypeString }, declaration.ErrReference},
		{"optional source", func(m *declaration.Member[access]) {
			m.Plans[0].Statements[0].Cardinality = declaration.CardinalityZeroOrOne
		}, declaration.ErrUnsupported},
		{"many source", func(m *declaration.Member[access]) {
			m.Plans[0].Statements[0].Cardinality = declaration.CardinalityMany
		}, declaration.ErrUnsupported},
		{"unused input", func(m *declaration.Member[access]) {
			m.Plans[0].Parameters = append(m.Plans[0].Parameters, declaration.Parameter{ID: "unused", Type: declaration.TypeString, GoField: "Unused"})
		}, declaration.ErrInvalidSchema},
		{"unused query parameter", func(m *declaration.Member[access]) {
			m.Queries[1].Parameters = append(m.Queries[1].Parameters, declaration.Parameter{ID: "unused", Type: declaration.TypeInt64})
			m.Plans[0].Statements[1].Bindings = append(m.Plans[0].Statements[1].Bindings, declaration.PlanBinding{Parameter: "unused", FromParameter: "account-id"})
		}, declaration.ErrInvalidSchema},
		{"duplicate input", func(m *declaration.Member[access]) {
			m.Plans[0].Parameters = append(m.Plans[0].Parameters, m.Plans[0].Parameters[0])
		}, declaration.ErrDuplicate},
		{"duplicate input field", func(m *declaration.Member[access]) {
			m.Plans[0].Parameters[1].GoField = m.Plans[0].Parameters[0].GoField
		}, declaration.ErrDuplicate},
		{"invalid input id", func(m *declaration.Member[access]) { m.Plans[0].Parameters[0].ID = "Bad" }, declaration.ErrInvalidSchema},
		{"invalid input type", func(m *declaration.Member[access]) { m.Plans[0].Parameters[0].Type = "invalid" }, declaration.ErrInvalidSchema},
		{"missing input field", func(m *declaration.Member[access]) { m.Plans[0].Parameters[0].GoField = "" }, declaration.ErrInvalidID},
		{"private input field", func(m *declaration.Member[access]) { m.Plans[0].Parameters[0].GoField = "digest" }, declaration.ErrInvalidID},
		{"imported query", func(m *declaration.Member[access]) {
			m.Imports = []string{"other.data/v1"}
			m.Plans[0].Statements[0].Query.Member = "other.data/v1"
		}, declaration.ErrUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			member := boundPlanMember()
			test.edit(&member)
			if _, err := member.Normalize(); !errors.Is(err, test.want) {
				t.Fatalf("Normalize = %v, want %v", err, test.want)
			}
		})
	}
}

func TestNormalizePlanOutputsAndAssertions(t *testing.T) {
	member := boundPlanMember()
	member.Plans[0].Outputs = nil
	if _, err := member.Normalize(); err != nil {
		t.Fatalf("zero-output plan rejected: %v", err)
	}
	tests := []struct {
		name string
		edit func(*declaration.Member[access])
		want error
	}{
		{"returning lacks affected", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Affected = declaration.AffectedRows{} }, declaration.ErrInvalidSchema},
		{"returning lacks cardinality", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Cardinality = "" }, declaration.ErrInvalidSchema},
		{"returning none", func(m *declaration.Member[access]) {
			m.Plans[0].Statements[0].Cardinality = declaration.CardinalityNone
		}, declaration.ErrInvalidSchema},
		{"ignored affected payload", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Affected.Set = false }, declaration.ErrInvalidSchema},
		{"invalid interval", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Affected.Min = 2 }, declaration.ErrInvalidSchema},
		{"unbounded max", func(m *declaration.Member[access]) { m.Plans[0].Statements[0].Affected.Unbounded = true }, declaration.ErrInvalidSchema},
		{"unknown output statement", func(m *declaration.Member[access]) { m.Plans[0].Outputs[0].Statement = "missing" }, declaration.ErrReference},
		{"no-row output statement", func(m *declaration.Member[access]) { m.Plans[0].Outputs[0].Statement = "finish" }, declaration.ErrUnsupported},
		{"duplicate output field", func(m *declaration.Member[access]) { m.Plans[0].Outputs[1].GoField = "AccountID" }, declaration.ErrDuplicate},
		{"duplicate output source", func(m *declaration.Member[access]) { m.Plans[0].Outputs[1].Statement = "account" }, declaration.ErrDuplicate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			member := boundPlanMember()
			test.edit(&member)
			if _, err := member.Normalize(); !errors.Is(err, test.want) {
				t.Fatalf("Normalize = %v, want %v", err, test.want)
			}
		})
	}
	member = boundPlanMember()
	member.Plans[0].Statements[0].Affected = declaration.AffectedRows{Set: true, Min: 1, Unbounded: true}
	if _, err := member.Normalize(); err != nil {
		t.Fatalf("explicit unbounded interval = %v", err)
	}
}

func TestNormalizePlanAllowsUnretainedMutationResults(t *testing.T) {
	for _, cardinality := range []declaration.Cardinality{declaration.CardinalityExactlyOne, declaration.CardinalityZeroOrOne, declaration.CardinalityMany} {
		member := boundPlanMember()
		member.Plans[0].Statements[1].Cardinality = cardinality
		member.Plans[0].Statements[1].Affected = declaration.AffectedRows{Set: true, Min: 0, Max: 2}
		member.Plans[0].Outputs = member.Plans[0].Outputs[:1]
		if _, err := member.Normalize(); err != nil {
			t.Fatalf("unretained RETURNING cardinality %s = %v", cardinality, err)
		}
	}
}

func boundPlanMember() declaration.Member[access] {
	return declaration.Member[access]{Namespace: "accounts", Tables: []declaration.Table{
		{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "name", Type: declaration.TypeString}, {ID: "note", Type: declaration.TypeString, Nullable: true}}},
		{ID: "credentials", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "account", Type: declaration.TypeInt64}, {ID: "digest", Type: declaration.TypeBytes}}},
	}, Queries: []declaration.Query{
		{ID: "create-account", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "accounts"}, Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}, {ID: "name", Type: declaration.TypeString}},
			Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}}, {Column: "name", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "name"}}},
			Returning:   []declaration.Projection{{Column: "id"}, {Column: "note"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}, {ID: "note", Source: "note", Type: declaration.TypeString, Nullable: true}}},
		{ID: "create-credential", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "credentials"}, Parameters: []declaration.Parameter{{ID: "account", Type: declaration.TypeInt64}, {ID: "id", Type: declaration.TypeInt64}, {ID: "digest", Type: declaration.TypeBytes}},
			Assignments: []declaration.Assignment{{Column: "account", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "account"}}, {Column: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}}, {Column: "digest", Value: declaration.Value{Kind: declaration.TypeBytes, Set: true, Parameter: "digest"}}},
			Returning:   []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}}},
		{ID: "finish-account", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"}, Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
			Assignments: []declaration.Assignment{{Column: "name", Value: declaration.String("registered")}}, Predicate: declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}}},
	}, Plans: []declaration.Plan{{ID: "register", Method: "Register", Parameters: []declaration.Parameter{{ID: "digest", Type: declaration.TypeBytes, GoField: "Digest"}, {ID: "name", Type: declaration.TypeString, GoField: "Name"}, {ID: "account-id", Type: declaration.TypeInt64, GoField: "AccountID"}, {ID: "credential-id", Type: declaration.TypeInt64, GoField: "CredentialID"}},
		Statements: []declaration.PlanStatement{
			{ID: "account", Query: declaration.Ref{Symbol: "create-account"}, Cardinality: declaration.CardinalityExactlyOne, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "name", FromParameter: "name"}, {Parameter: "id", FromParameter: "account-id"}}},
			{ID: "credential", Query: declaration.Ref{Symbol: "create-credential"}, Cardinality: declaration.CardinalityExactlyOne, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "account", FromStatement: "account", FromResult: "id"}, {Parameter: "id", FromParameter: "credential-id"}, {Parameter: "digest", FromParameter: "digest"}}},
			{ID: "finish", Query: declaration.Ref{Symbol: "finish-account"}, Cardinality: declaration.CardinalityNone, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromStatement: "account", FromResult: "id"}}},
		}, Outputs: []declaration.PlanOutput{{Statement: "account", Result: "id", GoField: "AccountID"}, {Statement: "credential", Result: "id", GoField: "CredentialID"}, {Statement: "account", Result: "note", GoField: "Note"}},
	}}}
}
