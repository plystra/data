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
	MemberID string
	Path     string
	Plan     database.Plan
	Digest   string
}

// CompilePlans lowers the initial executable-plan subset. It intentionally
// accepts only local, parameter-free SELECT statements so every statement can
// carry a complete typed result contract into the provider.
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
	statements := make([]database.Statement, len(plan.Statements))
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
		if query.Operation != declaration.OperationSelect {
			return PlanArtifact{}, fmt.Errorf("statement %s is not a SELECT", statement.ID)
		}
		if len(query.Parameters) != 0 || len(query.Joins) != 0 || query.Table.Member != "" {
			return PlanArtifact{}, fmt.Errorf("statement %s uses unsupported SELECT features", statement.ID)
		}
		if len(query.Projection) == 0 || len(query.Results) == 0 {
			return PlanArtifact{}, fmt.Errorf("statement %s requires typed projected results", statement.ID)
		}
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
		statements[index] = database.Statement{
			ID: statement.ID, SQL: queryArtifact.SQL, ReturnsRows: true,
			Cardinality: cardinality, ResultColumns: columns,
		}
	}
	runtimePlan := database.Plan{Statements: statements}
	if err := runtimePlan.Validate(); err != nil {
		return PlanArtifact{}, err
	}
	digest, err := digestPlan(runtimePlan)
	if err != nil {
		return PlanArtifact{}, err
	}
	return PlanArtifact{
		MemberID: member.MemberID,
		Path:     "plans/" + member.MemberID + "/" + plan.ID + ".json",
		Plan:     runtimePlan,
		Digest:   digest,
	}, nil
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
	case declaration.CardinalityExactlyOne:
		return database.CardinalityExactlyOne, nil
	case declaration.CardinalityZeroOrOne:
		return database.CardinalityZeroOrOne, nil
	case declaration.CardinalityMany:
		return database.CardinalityMany, nil
	default:
		return "", fmt.Errorf("SELECT cardinality is required")
	}
}

func resultColumns(query declaration.Query) ([]database.ResultColumn, error) {
	bySource := make(map[string]declaration.ResultField, len(query.Results))
	for _, result := range query.Results {
		bySource[result.Source] = result
	}
	columns := make([]database.ResultColumn, len(query.Projection))
	for index, projection := range query.Projection {
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

func digestPlan(plan database.Plan) (string, error) {
	payload, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("plystra.data.postgres-plan/v1\x00"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
