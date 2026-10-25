package declaration

import "fmt"

func validatePlan(plan Plan, imports map[string]struct{}, queries map[string]Query) error {
	if !validSymbol(plan.ID) || len(plan.Statements) == 0 || len(plan.Statements) > 64 {
		return fmt.Errorf("%w: plan %q", ErrInvalidSchema, plan.ID)
	}
	parameters := make(map[string]ScalarType, len(plan.Parameters))
	fields := make(map[string]struct{}, len(plan.Parameters))
	for _, parameter := range plan.Parameters {
		if !validSymbol(parameter.ID) || !validScalar(parameter.Type) {
			return fmt.Errorf("%w: plan parameter %q", ErrInvalidSchema, parameter.ID)
		}
		if _, exists := parameters[parameter.ID]; exists {
			return fmt.Errorf("%w: plan parameter %q", ErrDuplicate, parameter.ID)
		}
		parameters[parameter.ID] = parameter.Type
		if plan.Method != "" || parameter.GoField != "" {
			if !validExportedGoName(parameter.GoField) {
				return fmt.Errorf("%w: plan parameter %q Go field", ErrInvalidID, parameter.ID)
			}
			if _, exists := fields[parameter.GoField]; exists {
				return fmt.Errorf("%w: plan parameter Go field %q", ErrDuplicate, parameter.GoField)
			}
			fields[parameter.GoField] = struct{}{}
		}
	}
	previous := make(map[string]PlanStatement, len(plan.Statements))
	used := make(map[string]struct{}, len(parameters))
	for _, statement := range plan.Statements {
		if !validSymbol(statement.ID) {
			return fmt.Errorf("%w: plan statement %q", ErrInvalidID, statement.ID)
		}
		if _, exists := previous[statement.ID]; exists {
			return fmt.Errorf("%w: plan statement %q", ErrDuplicate, statement.ID)
		}
		if err := validateRef(statement.Query, imports); err != nil {
			return err
		}
		if statement.Query.Member != "" {
			return fmt.Errorf("%w: imported plan query %q", ErrUnsupported, statement.Query.Member)
		}
		query, exists := queries[statement.Query.Symbol]
		if !exists {
			return fmt.Errorf("%w: query %q", ErrReference, statement.Query.Symbol)
		}
		if err := validatePlanAssertions(statement, query); err != nil {
			return err
		}
		if err := validateQueryParameterCoverage(query); err != nil {
			return err
		}
		if err := validatePlanBindings(statement, query, parameters, previous, queries, used); err != nil {
			return err
		}
		if plan.Method != "" && query.Operation == OperationSelect && (len(query.Projection) == 0 || len(query.Results) == 0) {
			return fmt.Errorf("%w: callable plan %q requires typed SELECT results", ErrUnsupported, plan.ID)
		}
		previous[statement.ID] = statement
	}
	for _, parameter := range plan.Parameters {
		if _, exists := used[parameter.ID]; !exists {
			return fmt.Errorf("%w: unused plan parameter %q", ErrInvalidSchema, parameter.ID)
		}
	}
	if plan.Method == "" && len(plan.Outputs) != 0 {
		return fmt.Errorf("%w: plan %q has outputs without a method", ErrInvalidSchema, plan.ID)
	}
	outputFields := make(map[string]struct{}, len(plan.Outputs))
	outputResults := make(map[string]struct{}, len(plan.Outputs))
	for _, output := range plan.Outputs {
		if !validExportedGoName(output.GoField) {
			return fmt.Errorf("%w: plan output field %q", ErrInvalidID, output.GoField)
		}
		if _, err := planResult(output.Statement, output.Result, previous, queries); err != nil {
			return err
		}
		if _, exists := outputFields[output.GoField]; exists {
			return fmt.Errorf("%w: plan output field %q", ErrDuplicate, output.GoField)
		}
		key := output.Statement + "/" + output.Result
		if _, exists := outputResults[key]; exists {
			return fmt.Errorf("%w: plan output result %q", ErrDuplicate, key)
		}
		outputFields[output.GoField] = struct{}{}
		outputResults[key] = struct{}{}
	}
	return nil
}

func validatePlanAssertions(statement PlanStatement, query Query) error {
	switch statement.Cardinality {
	case CardinalityNone, CardinalityExactlyOne, CardinalityZeroOrOne, CardinalityMany:
	case "":
		return fmt.Errorf("%w: plan statement %q cardinality", ErrInvalidSchema, statement.ID)
	default:
		return fmt.Errorf("%w: cardinality %q", ErrUnsupported, statement.Cardinality)
	}
	affected := statement.Affected
	if (!affected.Set && affected != (AffectedRows{})) ||
		(affected.Set && ((affected.Unbounded && affected.Max != 0) || (!affected.Unbounded && affected.Min > affected.Max))) {
		return fmt.Errorf("%w: plan statement %q affected row range", ErrInvalidSchema, statement.ID)
	}
	if query.Operation == OperationSelect {
		if statement.Cardinality == CardinalityNone || affected.Set {
			return fmt.Errorf("%w: select plan statement %q assertion", ErrInvalidSchema, statement.ID)
		}
		return nil
	}
	if !affected.Set || (len(query.Returning) == 0) != (statement.Cardinality == CardinalityNone) {
		return fmt.Errorf("%w: mutation plan statement %q assertion", ErrInvalidSchema, statement.ID)
	}
	return nil
}

func validatePlanBindings(statement PlanStatement, query Query, parameters map[string]ScalarType, previous map[string]PlanStatement, queries map[string]Query, used map[string]struct{}) error {
	targets := make(map[string]ScalarType, len(query.Parameters))
	for _, parameter := range query.Parameters {
		targets[parameter.ID] = parameter.Type
	}
	bound := make(map[string]struct{}, len(statement.Bindings))
	for _, binding := range statement.Bindings {
		kind, exists := targets[binding.Parameter]
		if !exists {
			return fmt.Errorf("%w: plan binding parameter %q", ErrReference, binding.Parameter)
		}
		if _, exists := bound[binding.Parameter]; exists {
			return fmt.Errorf("%w: plan binding parameter %q", ErrDuplicate, binding.Parameter)
		}
		bound[binding.Parameter] = struct{}{}
		if binding.FromParameter != "" {
			if binding.FromStatement != "" || binding.FromResult != "" {
				return fmt.Errorf("%w: mixed plan binding sources", ErrInvalidSchema)
			}
			source, exists := parameters[binding.FromParameter]
			if !exists || source != kind {
				return fmt.Errorf("%w: plan binding source parameter %q type", ErrReference, binding.FromParameter)
			}
			used[binding.FromParameter] = struct{}{}
			continue
		}
		if binding.FromStatement == "" || binding.FromResult == "" {
			return fmt.Errorf("%w: incomplete plan binding source", ErrInvalidSchema)
		}
		result, err := planResult(binding.FromStatement, binding.FromResult, previous, queries)
		if err != nil {
			return err
		}
		if result.Nullable || result.Type != kind {
			return fmt.Errorf("%w: plan binding result %q type or nullability", ErrReference, binding.FromResult)
		}
	}
	if len(bound) != len(targets) {
		return fmt.Errorf("%w: statement %q query parameter bindings are incomplete", ErrInvalidSchema, statement.ID)
	}
	return nil
}

func planResult(statementID, resultID string, statements map[string]PlanStatement, queries map[string]Query) (ResultField, error) {
	statement, exists := statements[statementID]
	if !exists {
		return ResultField{}, fmt.Errorf("%w: plan result statement %q is not earlier or present", ErrReference, statementID)
	}
	if statement.Cardinality != CardinalityExactlyOne {
		return ResultField{}, fmt.Errorf("%w: plan result statement %q requires exactly-one cardinality", ErrUnsupported, statementID)
	}
	for _, result := range queries[statement.Query.Symbol].Results {
		if result.ID == resultID {
			return result, nil
		}
	}
	return ResultField{}, fmt.Errorf("%w: plan result %q", ErrReference, resultID)
}
