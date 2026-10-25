package compiler_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
	postgres "github.com/plystra/data/postgres/compiler"
)

func TestCompilePlansBindsInputsReturningAndMutationAssertions(t *testing.T) {
	member := atomicRegistrationMember()
	artifacts, err := postgres.CompilePlans([]postgres.SchemaMember{member})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Plan.Inputs != nil || len(artifacts[0].Definition.Parameters) != 4 {
		t.Fatalf("unbound template = %#v", artifacts)
	}
	plan := artifacts[0].Plan
	if !errors.Is(plan.Validate(), database.ErrInvalidPlan) {
		t.Fatal("unbound template became executable")
	}
	plan.Inputs = map[string]database.Argument{
		"account-id": database.Int64Argument(11), "email": database.StringArgument("a@example.test"),
		"session-id": database.Int64Argument(12), "digest": database.BytesArgument(nil),
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(plan.Statements) != 3 {
		t.Fatal("statement order or inventory changed")
	}
	first, second, last := plan.Statements[0], plan.Statements[1], plan.Statements[2]
	if first.Args[0].FromInput.ID != "email" || first.Args[1].FromInput.ID != "account-id" || !first.Affected.Set || !first.ReturnsRows || first.Cardinality != database.CardinalityExactlyOne {
		t.Fatalf("first statement = %#v", first)
	}
	if second.Args[2].FromResult == nil || second.Args[2].FromResult.Statement != "account" || second.Args[2].FromResult.Column != 1 {
		t.Fatalf("named result did not map to RETURNING order: %#v", second.Args)
	}
	if last.ReturnsRows || last.Cardinality != database.CardinalityNone || last.Affected != database.ExactAffectedRows(1) || len(last.ResultColumns) != 0 {
		t.Fatalf("mutation assertion = %#v", last)
	}
	if artifacts[0].Plan.Inputs != nil || member.Model.Plans[0].Parameters[0].ID != "session-id" {
		t.Fatal("compilation mutated authored intent or retained bound values")
	}
}

func TestCompilePlansDigestBindsInputsOutputsAndAssertions(t *testing.T) {
	compile := func(member postgres.SchemaMember) postgres.PlanArtifact {
		t.Helper()
		artifacts, err := postgres.CompilePlans([]postgres.SchemaMember{member})
		if err != nil {
			t.Fatal(err)
		}
		return artifacts[0]
	}
	first := compile(atomicRegistrationMember())
	reordered := atomicRegistrationMember()
	reordered.Model.Plans[0].Parameters[0], reordered.Model.Plans[0].Parameters[3] = reordered.Model.Plans[0].Parameters[3], reordered.Model.Plans[0].Parameters[0]
	reordered.Model.Plans[0].Statements[0].Bindings[0], reordered.Model.Plans[0].Statements[0].Bindings[1] = reordered.Model.Plans[0].Statements[0].Bindings[1], reordered.Model.Plans[0].Statements[0].Bindings[0]
	if other := compile(reordered); other.Digest != first.Digest || !reflect.DeepEqual(other.Plan, first.Plan) {
		t.Fatal("equivalent normalized intent changed template identity")
	}
	for _, edit := range []func(*declaration.Plan){
		func(plan *declaration.Plan) { plan.Parameters[0].GoField = "SessionIdentifier" },
		func(plan *declaration.Plan) { plan.Outputs[0].GoField = "Identifier" },
		func(plan *declaration.Plan) { plan.Statements[2].Affected.Max = 2 },
		func(plan *declaration.Plan) {
			plan.Statements[1].Bindings[2] = declaration.PlanBinding{Parameter: "owner", FromParameter: "account-id"}
		},
	} {
		member := atomicRegistrationMember()
		edit(&member.Model.Plans[0])
		if other := compile(member); other.Digest == first.Digest {
			t.Fatal("changed public plan contract retained its digest")
		}
	}
}

func atomicRegistrationMember() postgres.SchemaMember {
	return postgres.SchemaMember{MemberID: "accounts.user/v1", Resource: "database.primary", Model: declaration.Model{
		Namespace: "accounts",
		Tables: []declaration.Table{
			{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}}},
			{ID: "sessions", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "owner", Type: declaration.TypeInt64}, {ID: "digest", Type: declaration.TypeBytes}}},
		},
		Queries: []declaration.Query{
			{ID: "create-account", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "accounts"},
				Parameters:  []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}},
				Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}, {Column: "email", Value: declaration.Value{Set: true, Kind: declaration.TypeString, Parameter: "email"}}},
				Returning:   []declaration.Projection{{Column: "email"}, {Column: "id", Alias: "identifier"}}, Results: []declaration.ResultField{{ID: "id", Source: "identifier", Type: declaration.TypeInt64}, {ID: "email", Source: "email", Type: declaration.TypeString}}},
			{ID: "create-session", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "sessions"},
				Parameters:  []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}, {ID: "owner", Type: declaration.TypeInt64}, {ID: "digest", Type: declaration.TypeBytes}},
				Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}, {Column: "owner", Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "owner"}}, {Column: "digest", Value: declaration.Value{Set: true, Kind: declaration.TypeBytes, Parameter: "digest"}}},
				Returning:   []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}}},
			{ID: "finish", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"},
				Parameters:  []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
				Assignments: []declaration.Assignment{{Column: "email", Value: declaration.String("registered@example.test")}},
				Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Set: true, Kind: declaration.TypeInt64, Parameter: "id"}}},
		},
		Plans: []declaration.Plan{{ID: "register", Method: "Register",
			Parameters: []declaration.Parameter{{ID: "session-id", Type: declaration.TypeInt64, GoField: "SessionID"}, {ID: "email", Type: declaration.TypeString, GoField: "Email"}, {ID: "account-id", Type: declaration.TypeInt64, GoField: "AccountID"}, {ID: "digest", Type: declaration.TypeBytes, GoField: "Digest"}},
			Statements: []declaration.PlanStatement{
				{ID: "account", Query: declaration.Ref{Symbol: "create-account"}, Cardinality: declaration.CardinalityExactlyOne, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromParameter: "account-id"}, {Parameter: "email", FromParameter: "email"}}},
				{ID: "session", Query: declaration.Ref{Symbol: "create-session"}, Cardinality: declaration.CardinalityExactlyOne, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromParameter: "session-id"}, {Parameter: "digest", FromParameter: "digest"}, {Parameter: "owner", FromStatement: "account", FromResult: "id"}}},
				{ID: "finish", Query: declaration.Ref{Symbol: "finish"}, Cardinality: declaration.CardinalityNone, Affected: declaration.AffectedRows{Set: true, Min: 1, Max: 1}, Bindings: []declaration.PlanBinding{{Parameter: "id", FromStatement: "account", FromResult: "id"}}},
			}, Outputs: []declaration.PlanOutput{{Statement: "account", Result: "id", GoField: "AccountID"}, {Statement: "session", Result: "id", GoField: "SessionID"}},
		}},
	}}
}
