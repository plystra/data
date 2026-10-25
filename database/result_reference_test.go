package database_test

import (
	"errors"
	"math"
	"testing"

	"github.com/plystra/data/database"
)

func referencePlan(kind database.ValueKind) database.Plan {
	return database.Plan{Statements: []database.Statement{
		{ID: "create", SQL: "INSERT INTO records DEFAULT VALUES RETURNING id", ReturnsRows: true, Cardinality: database.CardinalityExactlyOne,
			Affected: database.ExactAffectedRows(1), ResultColumns: []database.ResultColumn{{Kind: kind}}},
		{ID: "dependent", SQL: "INSERT INTO children VALUES ($1)", Affected: database.ExactAffectedRows(1),
			Args: []database.Argument{database.ResultArgument(kind, "create", 0)}},
	}}
}

func TestPlanValidateAcceptsEarlierTypedResultBindings(t *testing.T) {
	for _, kind := range []database.ValueKind{database.ValueBool, database.ValueBytes, database.ValueFloat64, database.ValueInt64, database.ValueString} {
		if err := referencePlan(kind).Validate(); err != nil {
			t.Fatalf("valid reference kind %s rejected: %v", kind, err)
		}
	}
}

func TestPlanValidateRejectsInvalidResultBindings(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*database.Plan)
	}{
		{"forward", func(p *database.Plan) { p.Statements[0], p.Statements[1] = p.Statements[1], p.Statements[0] }},
		{"self", func(p *database.Plan) { p.Statements[1].Args[0].FromResult.Statement = "dependent" }},
		{"missing", func(p *database.Plan) { p.Statements[1].Args[0].FromResult.Statement = "missing" }},
		{"empty", func(p *database.Plan) { p.Statements[1].Args[0].FromResult.Statement = "" }},
		{"column", func(p *database.Plan) { p.Statements[1].Args[0].FromResult.Column = 1 }},
		{"column overflow", func(p *database.Plan) { p.Statements[1].Args[0].FromResult.Column = math.MaxUint32 }},
		{"type", func(p *database.Plan) { p.Statements[1].Args[0].Kind = database.ValueString }},
		{"unknown kind", func(p *database.Plan) { p.Statements[1].Args[0].Kind = "unknown" }},
		{"null kind", func(p *database.Plan) { p.Statements[1].Args[0].Kind = database.ValueNull }},
		{"missing columns", func(p *database.Plan) { p.Statements[0].ResultColumns = nil }},
		{"nullable", func(p *database.Plan) { p.Statements[0].ResultColumns[0].Nullable = true }},
		{"many", func(p *database.Plan) { p.Statements[0].Cardinality = database.CardinalityMany }},
		{"optional", func(p *database.Plan) { p.Statements[0].Cardinality = database.CardinalityZeroOrOne }},
		{"not a row source", func(p *database.Plan) {
			p.Statements[0].ReturnsRows = false
			p.Statements[0].Cardinality = database.CardinalityNone
			p.Statements[0].ResultColumns = nil
		}},
		{"literal string", func(p *database.Plan) { p.Statements[1].Args[0].String = "private" }},
		{"literal integer", func(p *database.Plan) { p.Statements[1].Args[0].Int64 = 1 }},
		{"literal float", func(p *database.Plan) { p.Statements[1].Args[0].Float64 = 1 }},
		{"literal bool", func(p *database.Plan) { p.Statements[1].Args[0].Bool = true }},
		{"literal bytes", func(p *database.Plan) { p.Statements[1].Args[0].Bytes = []byte{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := referencePlan(database.ValueInt64)
			test.edit(&plan)
			if err := plan.Validate(); !errors.Is(err, database.ErrInvalidPlan) {
				t.Fatalf("invalid binding accepted: %v", err)
			}
		})
	}
}

func TestPlanValidateReturningAffectedAssertions(t *testing.T) {
	for _, assertion := range []database.AffectedRows{database.ExactAffectedRows(0), database.ExactAffectedRows(1), database.AtLeastAffectedRows(1), {Set: true, Min: 1, Max: 3}} {
		plan := referencePlan(database.ValueInt64)
		plan.Statements[0].Affected = assertion
		if err := plan.Validate(); err != nil {
			t.Fatalf("valid returning assertion rejected: %v", err)
		}
	}
	for _, assertion := range []database.AffectedRows{{Min: 1}, {Unbounded: true}, {Set: true, Min: 2, Max: 1}} {
		plan := referencePlan(database.ValueInt64)
		plan.Statements[0].Affected = assertion
		if err := plan.Validate(); !errors.Is(err, database.ErrInvalidPlan) {
			t.Fatalf("invalid returning assertion accepted: %v", err)
		}
	}
}
