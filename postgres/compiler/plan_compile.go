package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
)

var ErrPlanCompilation = errors.New("PostgreSQL plan compilation failed")

// PlanArtifact is one executable, deterministic plan for a member assigned to
// one database Resource. The runtime plan contains no callbacks or transaction
// handles; the provider owns the transaction boundary.
type PlanArtifact struct {
	MemberID   string
	ID         string
	Path       string
	Definition declaration.Plan
	Plan       database.Plan
	Digest     string
}

// CompilePlans lowers ordered local queries into one atomic plan template.
// Request inputs remain unbound. Row-producing statements carry complete typed
// results, and mutations retain their independent affected-row assertions.
func CompilePlans(members []SchemaMember) ([]PlanArtifact, error) {
	normalized, _, err := normalizeQueryMembers(members)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPlanCompilation, err)
	}
	queries, err := CompileQueries(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPlanCompilation, err)
	}
	byQuery := make(map[string]QueryArtifact, len(queries))
	for _, query := range queries {
		byQuery[query.MemberID+"\x00"+query.Path] = query
	}
	artifacts := make([]PlanArtifact, 0)
	for _, member := range normalized {
		for _, plan := range member.Model.Plans {
			compiled, err := compilePlan(member, plan, byQuery)
			if err != nil {
				return nil, fmt.Errorf("%w: plan %s: %v", ErrPlanCompilation, plan.ID, err)
			}
			artifacts = append(artifacts, compiled)
		}
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts, nil
}

func compilePlan(member SchemaMember, plan declaration.Plan, byQuery map[string]QueryArtifact) (PlanArtifact, error) {
	if len(plan.Parameters) > database.MaximumInputs {
		return PlanArtifact{}, fmt.Errorf("too many plan inputs")
	}
	statements := make([]database.Statement, len(plan.Statements))
	previous := make(map[string]declaration.Query, len(plan.Statements))
	for index, statement := range plan.Statements {
		if statement.Query.Member != "" {
			return PlanArtifact{}, fmt.Errorf("statement %s references an imported query", statement.ID)
		}
		if statement.Query.Symbol == "" {
			return PlanArtifact{}, fmt.Errorf("statement %s has no query", statement.ID)
		}
		query, found := findQuery(member.Model.Queries, statement.Query.Symbol)
		if !found {
			return PlanArtifact{}, fmt.Errorf("statement %s references query %s", statement.ID, statement.Query.Symbol)
		}
		returnsRows := query.Operation == declaration.OperationSelect || len(query.Returning) != 0
		columns, err := resultColumns(query)
		if err != nil {
			return PlanArtifact{}, fmt.Errorf("statement %s: %v", statement.ID, err)
		}
		queryArtifact, found := byQuery[member.MemberID+"\x00"+"queries/"+member.MemberID+"/"+query.ID+".sql"]
		if !found {
			return PlanArtifact{}, fmt.Errorf("statement %s has no compiled query", statement.ID)
		}
		cardinality, err := planCardinality(statement.Cardinality)
		if err != nil {
			return PlanArtifact{}, fmt.Errorf("statement %s: %v", statement.ID, err)
		}
		if returnsRows && len(columns) == 0 {
			return PlanArtifact{}, fmt.Errorf("statement %s requires typed projected results", statement.ID)
		}
		arguments, err := planArguments(query, statement.Bindings, previous)
		if err != nil {
			return PlanArtifact{}, fmt.Errorf("statement %s: %v", statement.ID, err)
		}
		statements[index] = database.Statement{
			ID: statement.ID, SQL: queryArtifact.SQL, ReturnsRows: returnsRows,
			Args: arguments, Cardinality: cardinality, ResultColumns: columns,
			Affected: database.AffectedRows{Set: statement.Affected.Set, Min: statement.Affected.Min, Max: statement.Affected.Max, Unbounded: statement.Affected.Unbounded},
		}
		previous[statement.ID] = query
	}
	runtimePlan := database.Plan{Statements: statements}
	// Validate with typed probes without retaining request values or making an
	// unbound template executable with invented zero-valued inputs.
	probe := runtimePlan
	probe.Inputs = make(map[string]database.Argument, len(plan.Parameters))
	for _, parameter := range plan.Parameters {
		kind, ok := databaseValueKind(parameter.Type)
		if !ok {
			return PlanArtifact{}, fmt.Errorf("input %s has unsupported type %q", parameter.ID, parameter.Type)
		}
		probe.Inputs[parameter.ID] = database.Argument{Kind: kind}
	}
	if err := probe.Validate(); err != nil {
		return PlanArtifact{}, err
	}
	digest, err := digestPlan(runtimePlan, plan)
	if err != nil {
		return PlanArtifact{}, err
	}
	return PlanArtifact{
		MemberID: member.MemberID, ID: plan.ID,
		Path:       "plans/" + member.MemberID + "/" + plan.ID + ".json",
		Definition: plan,
		Plan:       runtimePlan,
		Digest:     digest,
	}, nil
}

func planArguments(query declaration.Query, bindings []declaration.PlanBinding, previous map[string]declaration.Query) ([]database.Argument, error) {
	byParameter := make(map[string]declaration.PlanBinding, len(bindings))
	for _, binding := range bindings {
		byParameter[binding.Parameter] = binding
	}
	arguments := make([]database.Argument, len(query.Parameters))
	for index, parameter := range query.Parameters {
		kind, ok := databaseValueKind(parameter.Type)
		if !ok {
			return nil, fmt.Errorf("parameter %s has unsupported type %q", parameter.ID, parameter.Type)
		}
		binding, ok := byParameter[parameter.ID]
		if !ok {
			return nil, fmt.Errorf("parameter %s is unbound", parameter.ID)
		}
		if binding.FromParameter != "" {
			arguments[index] = database.InputArgument(kind, binding.FromParameter)
			continue
		}
		source, ok := previous[binding.FromStatement]
		if !ok {
			return nil, fmt.Errorf("result statement %s is not earlier", binding.FromStatement)
		}
		column, err := planResultColumn(source, binding.FromResult)
		if err != nil {
			return nil, err
		}
		arguments[index] = database.ResultArgument(kind, binding.FromStatement, uint32(column))
	}
	return arguments, nil
}

func planResultColumn(query declaration.Query, id string) (int, error) {
	for _, result := range query.Results {
		if result.ID != id {
			continue
		}
		for index, projection := range queryProjections(query) {
			name := projection.Column
			if projection.Alias != "" {
				name = projection.Alias
			}
			if name == result.Source {
				return index, nil
			}
		}
	}
	return 0, fmt.Errorf("result %s has no projected column", id)
}

func findQuery(queries []declaration.Query, id string) (declaration.Query, bool) {
	for _, query := range queries {
		if query.ID == id {
			return query, true
		}
	}
	return declaration.Query{}, false
}

func planCardinality(value declaration.Cardinality) (database.Cardinality, error) {
	switch value {
	case declaration.CardinalityNone:
		return database.CardinalityNone, nil
	case declaration.CardinalityExactlyOne:
		return database.CardinalityExactlyOne, nil
	case declaration.CardinalityZeroOrOne:
		return database.CardinalityZeroOrOne, nil
	case declaration.CardinalityMany:
		return database.CardinalityMany, nil
	default:
		return "", fmt.Errorf("statement cardinality is required")
	}
}

func resultColumns(query declaration.Query) ([]database.ResultColumn, error) {
	bySource := make(map[string]declaration.ResultField, len(query.Results))
	for _, result := range query.Results {
		bySource[result.Source] = result
	}
	projections := queryProjections(query)
	columns := make([]database.ResultColumn, len(projections))
	for index, projection := range projections {
		name := projection.Column
		if projection.Alias != "" {
			name = projection.Alias
		}
		result, ok := bySource[name]
		if !ok {
			return nil, fmt.Errorf("missing result mapping for %s", name)
		}
		kind, ok := databaseValueKind(result.Type)
		if !ok {
			return nil, fmt.Errorf("result %s has unsupported type %q", result.ID, result.Type)
		}
		columns[index] = database.ResultColumn{Kind: kind, Nullable: result.Nullable}
	}
	return columns, nil
}

func queryProjections(query declaration.Query) []declaration.Projection {
	if query.Operation == declaration.OperationSelect {
		return query.Projection
	}
	return query.Returning
}

func databaseValueKind(value declaration.ScalarType) (database.ValueKind, bool) {
	switch value {
	case declaration.TypeString:
		return database.ValueString, true
	case declaration.TypeInt64:
		return database.ValueInt64, true
	case declaration.TypeFloat64:
		return database.ValueFloat64, true
	case declaration.TypeBool:
		return database.ValueBool, true
	case declaration.TypeBytes:
		return database.ValueBytes, true
	default:
		return "", false
	}
}

func digestPlan(plan database.Plan, definition declaration.Plan) (string, error) {
	payload, err := json.Marshal(struct {
		Definition declaration.Plan
		Plan       database.Plan
	}{definition, plan})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("plystra.data.postgres-plan/v2\x00"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
