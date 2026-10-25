package database_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/plystra/data/database"
)

func inputPlan(value database.Argument) database.Plan {
	return database.Plan{Inputs: map[string]database.Argument{"value": value}, Statements: []database.Statement{{
		ID: "write", SQL: "INSERT INTO records VALUES ($1,$2)", Affected: database.ExactAffectedRows(1),
		Args: []database.Argument{database.InputArgument(value.Kind, "value"), database.InputArgument(value.Kind, "value")},
	}}}
}

func TestPlanValidateAcceptsBoundInputs(t *testing.T) {
	for _, value := range []database.Argument{
		database.StringArgument("bound-value"), database.Int64Argument(42), database.Float64Argument(1.5), database.BoolArgument(true), database.BytesArgument([]byte{0, 255}),
		database.StringArgument(""), database.Int64Argument(0), database.Float64Argument(0), database.BoolArgument(false), database.BytesArgument(nil),
	} {
		if err := inputPlan(value).Validate(); err != nil {
			t.Fatalf("valid input kind %s rejected: %v", value.Kind, err)
		}
	}
	plan := referencePlan(database.ValueInt64)
	plan.Inputs = map[string]database.Argument{"label": database.StringArgument("parent")}
	plan.Statements[0].Args = []database.Argument{database.InputArgument(database.ValueString, "label")}
	if err := plan.Validate(); err != nil {
		t.Fatalf("inputs and earlier result references could not compose: %v", err)
	}
}

func TestPlanValidateRejectsInvalidBoundInputs(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*database.Plan)
	}{
		{"missing", func(p *database.Plan) { p.Inputs = nil }},
		{"unused", func(p *database.Plan) { p.Inputs["unused"] = database.BoolArgument(true) }},
		{"empty reference", func(p *database.Plan) { p.Statements[0].Args[0] = database.InputArgument(database.ValueInt64, "") }},
		{"unknown reference", func(p *database.Plan) { p.Statements[0].Args[0].FromInput.ID = "unknown" }},
		{"empty identity", func(p *database.Plan) { p.Inputs[""] = database.Int64Argument(0) }},
		{"invalid identity", func(p *database.Plan) { p.Inputs[" trailing "] = database.Int64Argument(0) }},
		{"oversize identity", func(p *database.Plan) { p.Inputs[fmt.Sprintf("%0129d", 0)] = database.Int64Argument(0) }},
		{"null", func(p *database.Plan) { p.Inputs["value"] = database.NullArgument() }},
		{"unknown scalar", func(p *database.Plan) { p.Inputs["value"] = database.Argument{Kind: "unknown"} }},
		{"wrong type", func(p *database.Plan) { p.Inputs["value"] = database.StringArgument("42") }},
		{"wrong reference type", func(p *database.Plan) { p.Statements[0].Args[0].Kind = database.ValueString }},
		{"nested input", func(p *database.Plan) { p.Inputs["value"] = database.InputArgument(database.ValueInt64, "value") }},
		{"nested result", func(p *database.Plan) { p.Inputs["value"] = database.ResultArgument(database.ValueInt64, "earlier", 0) }},
		{"mixed source", func(p *database.Plan) {
			p.Statements[0].Args[0].FromResult = &database.ResultReference{Statement: "earlier"}
		}},
		{"reference string", func(p *database.Plan) { p.Statements[0].Args[0].String = "unconsumed" }},
		{"reference integer", func(p *database.Plan) { p.Statements[0].Args[0].Int64 = 42 }},
		{"reference float", func(p *database.Plan) { p.Statements[0].Args[0].Float64 = 1.5 }},
		{"reference bool", func(p *database.Plan) { p.Statements[0].Args[0].Bool = true }},
		{"reference bytes", func(p *database.Plan) { p.Statements[0].Args[0].Bytes = []byte{} }},
		{"literal cross-field", func(p *database.Plan) {
			p.Inputs["value"] = database.Argument{Kind: database.ValueInt64, Int64: 42, String: "unconsumed"}
		}},
		{"literal empty bytes cross-field", func(p *database.Plan) {
			p.Inputs["value"] = database.Argument{Kind: database.ValueInt64, Bytes: []byte{}}
		}},
		{"inventory limit", func(p *database.Plan) {
			for i := range database.MaximumInputs + 1 {
				p.Inputs[fmt.Sprintf("input-%d", i)] = database.Int64Argument(0)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := inputPlan(database.Int64Argument(42))
			test.edit(&plan)
			if err := plan.Validate(); !errors.Is(err, database.ErrInvalidPlan) {
				t.Fatalf("invalid bound input accepted: %v", err)
			}
		})
	}
}
